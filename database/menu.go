package database

import (
	"log"

	"server/cache"
	"server/models"
)

// BuildMenuTree 将扁平菜单列表构建成树
func BuildTree(menus []models.Menu) []*models.Menu {
	// id -> node
	nodeMap := make(map[uint]*models.Menu)
	roots := make([]*models.Menu, 0)
	for i := range menus {
		m := menus[i]
		mm := m
		mm.Children = make([]*models.Menu, 0)
		nodeMap[mm.ID] = &mm
	}
	for _, m := range menus {
		mm := nodeMap[m.ID]
		if m.ParentID == 0 || nodeMap[m.ParentID] == nil {
			roots = append(roots, mm)
		} else {
			nodeMap[m.ParentID].Children = append(nodeMap[m.ParentID].Children, mm)
		}
	}
	return roots
}

// SortTreeChildren 按 Sort 升序对该层节点及其后代排序（递归）
func SortTree(nodes []*models.Menu) {
	if len(nodes) == 0 {
		return
	}
	for i := 1; i < len(nodes); i++ {
		for j := 0; j < len(nodes)-i; j++ {
			if nodes[j].Sort > nodes[j+1].Sort {
				nodes[j], nodes[j+1] = nodes[j+1], nodes[j]
			}
		}
	}
	for _, n := range nodes {
		SortTree(n.Children)
	}
}

// GetRoleMenuIDs 获取角色已绑定的菜单 ID
func GetRoleMenuIDs(roleID uint) []uint {
	ids := make([]uint, 0)
	DB.Model(&models.RoleMenu{}).Where("role_id = ?", roleID).Pluck("menu_id", &ids)
	return ids
}

// GetRoleMenuTree 返回角色对应的菜单树（不含按钮权限）
// 优先走 Redis 缓存（menu 变更时由 controllers.InvalidateMenus 失效），miss 回源数据库并回填。
func GetRoleMenuTree(roleID uint) []*models.Menu {
	tree, _ := cache.MenuTreeByRole(roleID, func() ([]*models.Menu, error) {
		ids := GetRoleMenuIDs(roleID)
		if len(ids) == 0 {
			return []*models.Menu{}, nil
		}
		menus := make([]models.Menu, 0)
		// 只取目录和菜单类型（不含按钮，按钮用于前端权限标识）
		DB.Where("id IN ? AND type IN ?", ids, []string{"M", "C"}).
			Order("sort asc").Find(&menus)
		t := BuildTree(menus)
		SortTree(t)
		return t, nil
	})
	return tree
}

// GetRolePermCodes 返回角色拥有的所有权限标识（含按钮）
// 优先走 Redis 缓存（角色-菜单绑定变更时由 controllers.InvalidatePerms 失效），
// 避免鉴权 Perm() 中间件每请求 3 条 SQL。
func GetRolePermCodes(roleID uint) map[string]bool {
	perms, _ := cache.PermsByRole(roleID, func() (map[string]bool, error) {
		ids := GetRoleMenuIDs(roleID)
		res := make(map[string]bool)
		if len(ids) == 0 {
			return res, nil
		}
		permsList := make([]string, 0)
		DB.Model(&models.Menu{}).Where("id IN ? AND perms <> ''", ids).Pluck("perms", &permsList)
		for _, p := range permsList {
			res[p] = true
		}
		return res, nil
	})
	return perms
}

// GetAllMenuTree 返回全部菜单树（走 Redis 缓存，菜单变更时由 InvalidateMenus 失效）
func GetAllMenuTree() []*models.Menu {
	tree, _ := cache.MenuTreeAll(func() ([]*models.Menu, error) {
		menus := make([]models.Menu, 0)
		DB.Order("sort asc").Find(&menus)
		t := BuildTree(menus)
		SortTree(t)
		return t, nil
	})
	return tree
}

// pruneDisabled 递归剔除已停用(status=0)的菜单节点
func pruneDisabled(nodes []*models.Menu) []*models.Menu {
	out := make([]*models.Menu, 0, len(nodes))
	for _, n := range nodes {
		if n.Status == 0 {
			continue
		}
		n.Children = pruneDisabled(n.Children)
		out = append(out, n)
	}
	return out
}

// GetEnabledMenuTree 返回启用(status=1)的菜单树（当前用户可见菜单，
// 供登录信息/侧边栏使用：停用的菜单不再显示）
func GetEnabledMenuTree(isSuper bool, roleID uint) []*models.Menu {
	if isSuper {
		return pruneDisabled(GetAllMenuTree())
	}
	return pruneDisabled(GetRoleMenuTree(roleID))
}

// dedupMenus 启动自愈：合并 menus 表中按 (parent_id, name) 重复的节点（数据脏，
// 如 EnsureTradeMenus 对存量库补菜单时重复插入的副本），只保留最小 id 的那份。
// 仅 name 非空的目录/菜单参与去重；按钮（name 为空）靠 perms/title 区分，不合并。
// 会同步收敛重复节点下的子菜单挂载与角色绑定，并失效菜单/权限缓存。
func dedupMenus() {
	if DB == nil {
		return
	}
	var menus []models.Menu
	DB.Order("id asc").Find(&menus)
	type key struct{ parent uint; name string }
	by := map[key][]models.Menu{}
	for _, m := range menus {
		if m.Name == "" {
			continue
		}
		k := key{m.ParentID, m.Name}
		by[k] = append(by[k], m)
	}
	changed := false
	for _, list := range by {
		if len(list) <= 1 {
			continue
		}
		keep := list[0].ID // 最小 id 为保留节点
		for _, dup := range list[1:] {
			// 重复节点下的子菜单/按钮重新挂到保留节点
			DB.Model(&models.Menu{}).Where("parent_id = ?", dup.ID).Update("parent_id", keep)
			// 角色绑定合并：角色已有 keep 绑定则删 dup 绑定，否则改为 keep
			var rms []models.RoleMenu
			DB.Where("menu_id = ?", dup.ID).Find(&rms)
			for _, rm := range rms {
				var cnt int64
				DB.Model(&models.RoleMenu{}).
					Where("role_id = ? AND menu_id = ?", rm.RoleID, keep).Count(&cnt)
				if cnt > 0 {
					DB.Where("role_id = ? AND menu_id = ?", rm.RoleID, rm.MenuID).
						Delete(&models.RoleMenu{})
				} else {
					DB.Model(&models.RoleMenu{}).
						Where("role_id = ? AND menu_id = ?", rm.RoleID, rm.MenuID).
						Update("menu_id", keep)
				}
			}
			DB.Delete(&models.Menu{}, dup.ID)
			changed = true
			log.Printf("[menu] dedup: %q(parent=%d) removed dup id=%d keep=%d", dup.Name, dup.ParentID, dup.ID, keep)
		}
	}
	if changed {
		cache.InvalidateMenus()
		cache.InvalidatePerms()
	}
}

// HasPerm 判断角色是否拥有某权限（isSuper 直接放行）
func HasPerm(roleID uint, isSuper bool, code string) bool {
	if isSuper {
		return true
	}
	return GetRolePermCodes(roleID)[code]
}

// ensureTradeButtons 为“交易管理”下每个 C 类型子菜单补齐按钮权限(F)：新增/编辑/删除，
// 权限码 = 子菜单 perms + ":add" / ":edit" / ":delete"（系统菜单在 seed 里带按钮，
// 交易菜单由 EnsureTradeMenus 后来补齐时只建了菜单、没建按钮，这里启动时兜底补上）。
// 与 EnsureTradeMenus 同款幂等：按 parent_id+perms 判存在、只补缺不删改；
// 绑定范围 = 已绑定该子菜单的角色，保证非超管角色也能分配按钮。
func ensureTradeButtons() {
	if DB == nil {
		return
	}
	var dir models.Menu
	if err := DB.Where("parent_id = 0 AND (name = ? OR path = '/trade')", "trade").First(&dir).Error; err != nil {
		return
	}
	var subs []models.Menu
	DB.Where("parent_id = ? AND type = ?", dir.ID, "C").Find(&subs)
	type action struct{ act, title string }
	actions := []action{{"add", "新增"}, {"edit", "编辑"}, {"delete", "删除"}}
	created := false
	for _, sub := range subs {
		if sub.Perms == "" {
			continue
		}
		var roleIDs []uint
		DB.Model(&models.RoleMenu{}).Where("menu_id = ?", sub.ID).Pluck("role_id", &roleIDs)
		// 绑定某个按钮的子过程（幂等：按 parent_id+perms 判存在）
		bindButton := func(title, perm string, sort int) {
			var cnt int64
			DB.Model(&models.Menu{}).Where("parent_id = ? AND perms = ?", sub.ID, perm).Count(&cnt)
			if cnt > 0 {
				return
			}
			btn := models.Menu{
				ParentID: sub.ID, Title: title, Perms: perm, Type: "F",
				Sort: sort, Visible: 1, Status: 1,
			}
			if err := DB.Create(&btn).Error; err != nil {
				log.Printf("[menu] create trade button %s err: %v", perm, err)
				return
			}
			for _, rid := range roleIDs {
				var bc int64
				DB.Model(&models.RoleMenu{}).Where("role_id = ? AND menu_id = ?", rid, btn.ID).Count(&bc)
				if bc == 0 {
					DB.Create(&models.RoleMenu{RoleID: rid, MenuID: btn.ID})
				}
			}
			created = true
			log.Printf("[menu] ensure trade button %q(%s) under %q", title, perm, sub.Name)
		}
		for i, a := range actions {
			bindButton(a.title, sub.Perms+":"+a.act, i+1)
		}
		// 出入金目录：提现接口单独用 trade:withdraw，补一个"提现审核"按钮
		if sub.Perms == "trade:deposit" {
			bindButton("提现审核", "trade:withdraw:edit", len(actions)+1)
		}
	}
	if created {
		cache.InvalidateMenus()
		cache.InvalidatePerms()
	}
}

// ensureAppMenus 幂等补齐「App管理」顶层目录 + 其下「Banner管理」菜单 + 新增/编辑/删除按钮，
// 并绑定到所有存量角色（非超管也能在角色授权里分配）。同 EnsureTradeMenus：只补缺、不删改。
func ensureAppMenus() {
	if DB == nil {
		return
	}
	// 1. 顶层目录
	var dir models.Menu
	created := false
	if err := DB.Where("parent_id = 0 AND (name = ? OR path = '/app')", "app").First(&dir).Error; err != nil {
		dir = models.Menu{
			ParentID: 0, Name: "app", Title: "App管理", Path: "/app",
			Icon: "Grid", Type: "M", Sort: 4, Visible: 1, Status: 1,
		}
		if err := DB.Create(&dir).Error; err != nil {
			log.Println("[menu] create app dir err:", err)
			return
		}
		created = true
	}
	// 2. 子菜单 banner
	var child models.Menu
	if err := DB.Where("parent_id = ? AND name = ?", dir.ID, "banner").First(&child).Error; err != nil {
		child = models.Menu{
			ParentID: dir.ID, Name: "banner", Title: "Banner管理", Path: "/app/banner",
			Component: "app/banner", Icon: "Picture", Perms: "app:banner",
			Type: "C", Sort: 1, Visible: 1, Status: 1,
		}
		if err := DB.Create(&child).Error; err != nil {
			log.Println("[menu] create app/banner menu err:", err)
			return
		}
		created = true
	}
	// 3. 按钮（幂等：parent+perms 判存在）
	actions := []struct{ act, title string }{{"add", "新增"}, {"edit", "编辑"}, {"delete", "删除"}}
	menuIDs := []uint{dir.ID, child.ID}
	for i, a := range actions {
		perm := "app:banner:" + a.act
		var cnt int64
		DB.Model(&models.Menu{}).Where("parent_id = ? AND perms = ?", child.ID, perm).Count(&cnt)
		if cnt > 0 {
			continue
		}
		btn := models.Menu{
			ParentID: child.ID, Title: a.title, Perms: perm, Type: "F",
			Sort: i + 1, Visible: 1, Status: 1,
		}
		if err := DB.Create(&btn).Error; err != nil {
			log.Println("[menu] create app/banner button err:", err)
			continue
		}
		menuIDs = append(menuIDs, btn.ID)
		created = true
		log.Printf("[menu] ensure app/banner button %q(%s)", a.title, perm)
	}
	// 4. 把目录+子菜单+按钮绑定到所有存量角色（只补缺不删）
	var roles []models.Role
	DB.Model(&models.Role{}).Find(&roles)
	for _, r := range roles {
		for _, mid := range menuIDs {
			var c int64
			DB.Model(&models.RoleMenu{}).Where("role_id = ? AND menu_id = ?", r.ID, mid).Count(&c)
			if c == 0 {
				DB.Create(&models.RoleMenu{RoleID: r.ID, MenuID: mid})
			}
		}
	}
	if created {
		cache.InvalidateMenus()
		cache.InvalidatePerms()
	}
}

// EnsureTradeMenus 幂等地补齐“交易管理”目录及其子菜单（升级/补数用）。
// seed() 只在空库（0 个角色）时执行一次，存量库不会自动补菜单，
// 这里用存在性判断保证每次启动都能把缺失的交易菜单补齐，重复执行安全。
// 新补的菜单同时给存量角色做 role_menus 绑定（与 seed 的全量绑定保持一致），
// 否则非超管角色（如“账单”）在交易管理下会看不到流水/通知等新菜单。
func EnsureTradeMenus() {
	if DB == nil {
		return
	}
	var dirCount int64
	DB.Model(&models.Menu{}).
		Where("parent_id = 0 AND (name = ? OR path = '/trade')", "trade").
		Count(&dirCount)

	dirID := uint(0)
	if dirCount == 0 {
		dir := models.Menu{
			ParentID: 0, Name: "trade", Title: "交易管理", Path: "/trade",
			Icon: "TrendCharts", Type: "M", Sort: 3, Visible: 1, Status: 1,
		}
		if err := DB.Create(&dir).Error; err != nil {
			log.Println("[seed] create trade dir err:", err)
		} else {
			dirID = dir.ID
		}
	} else {
		var dir models.Menu
		DB.Where("parent_id = 0 AND (name = ? OR path = '/trade')", "trade").First(&dir)
		dirID = dir.ID
	}
	if dirID == 0 {
		return
	}

	children := []models.Menu{
		{Name: "tradeProduct", Title: "品种管理", Path: "/trade/product", Component: "trade/product", Icon: "Goods", Perms: "trade:product"},
		{Name: "tradeUser", Title: "用户管理", Path: "/trade/user", Component: "trade/user", Icon: "User", Perms: "trade:user"},
		{Name: "tradeOrder", Title: "订单管理", Path: "/trade/order", Component: "trade/order", Icon: "Tickets", Perms: "trade:order"},
		{Name: "tradePosition", Title: "持仓管理", Path: "/trade/position", Component: "trade/position", Icon: "Box", Perms: "trade:position"},
		{Name: "tradeFund", Title: "出入金管理", Path: "/trade/fund", Component: "trade/fund", Icon: "Wallet", Perms: "trade:deposit"},
		{Name: "tradePayAddr", Title: "收款地址管理", Path: "/trade/payaddress", Component: "trade/payaddress", Icon: "CreditCard", Perms: "trade:payaddress"},
		{Name: "tradeFlow", Title: "流水管理", Path: "/trade/flow", Component: "trade/flow", Icon: "List", Perms: "trade:flow"},
		{Name: "tradeNotice", Title: "通知管理", Path: "/trade/notice", Component: "trade/notice", Icon: "Bell", Perms: "trade:notice"},
		{Name: "tradeControl", Title: "控盘管理", Path: "/trade/control", Component: "trade/control", Icon: "Operation", Perms: "trade:control"},
		{Name: "tradeAgent", Title: "代理商管理", Path: "/trade/agent", Component: "trade/agent", Icon: "OfficeBuilding", Perms: "trade:agent"},
	}

	childIDs := make([]uint, 0, len(children))
	for i, ch := range children {
		var existing models.Menu
		err := DB.Where("parent_id = ? AND name = ?", dirID, ch.Name).First(&existing)
		if err == nil {
			// 已存在：同步排序，保证子菜单顺序与定义一致
			if existing.Sort != i+1 {
				DB.Model(&models.Menu{}).Where("id = ?", existing.ID).Update("sort", i+1)
			}
			childIDs = append(childIDs, existing.ID)
			continue
		}
		ch.ParentID = dirID
		ch.Sort = i + 1
		ch.Type = "C"
		ch.Visible = 1
		ch.Status = 1
		if err := DB.Create(&ch).Error; err != nil {
			log.Println("[seed] create trade menu err:", err)
		} else {
			childIDs = append(childIDs, ch.ID)
		}
	}

	// 给所有存量角色绑定上述交易菜单（目录 + 子菜单），只补缺、不删旧绑定
	var roles []models.Role
	DB.Model(&models.Role{}).Find(&roles)
	for _, r := range roles {
		bindIDs := append([]uint{dirID}, childIDs...)
		for _, mid := range bindIDs {
			var cnt int64
			DB.Model(&models.RoleMenu{}).
				Where("role_id = ? AND menu_id = ?", r.ID, mid).
				Count(&cnt)
			if cnt == 0 {
				DB.Create(&models.RoleMenu{RoleID: r.ID, MenuID: mid})
			}
		}
	}
}
