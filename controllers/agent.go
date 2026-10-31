package controllers

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"server/cache"
	"server/database"
	"server/middleware"
	"server/models"
	"server/services/trader"
	"server/utils"
)

// AgentLogin 代理商登录
func AgentLogin(c *gin.Context) {
	var req LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请输入账号和密码")
		return
	}
	var agent models.Agent
	if err := database.DB.Where("username = ?", req.Username).First(&agent).Error; err != nil {
		utils.BadRequest(c, "账号不存在")
		return
	}
	if agent.Status != 1 {
		utils.BadRequest(c, "该代理商已被禁用")
		return
	}
	if !database.CheckPwd(agent.Password, req.Password) {
		utils.BadRequest(c, "密码错误")
		return
	}
	now := time.Now()
	database.DB.Model(&agent).Update("last_login", now)
	token, err := utils.SignAgentToken(agent.ID, agent.Username)
	if err != nil {
		utils.ServerError(c, "生成令牌失败")
		return
	}
	utils.OK(c, gin.H{"token": token, "agent": agent})
}

// AgentInfo 代理商信息
func AgentInfo(c *gin.Context) {
	a := middleware.GetAgent(c)
	var w models.Agent
	database.DB.First(&w, a.ID)
	utils.OK(c, w)
}

// AgentPromoBase 代理推广链接前缀（管理端「系统设置→网站设置→用户端地址」配置）
func AgentPromoBase(c *gin.Context) {
	utils.OK(c, gin.H{"promoBase": settingValue("clientSiteUrl", "")})
}

// AgentDashboard 代理数据看板
func AgentDashboard(c *gin.Context) {
	agent := middleware.GetAgent(c)

	// 名下用户数
	var userCount int64
	database.DB.Model(&models.User{}).Where("agent_id = ?", agent.ID).Count(&userCount)

	// 名下用户 ID 列表（供后续各查询复用）
	var uids []uint
	database.DB.Model(&models.User{}).Where("agent_id = ?", agent.ID).Pluck("id", &uids)

	// 名下用户总订单量
	var orderCount int64
	if len(uids) > 0 {
		database.DB.Model(&models.Order{}).Where("user_id IN ?", uids).Count(&orderCount)
	}

	// 今日新增用户数
	todayStart := time.Now().Truncate(24 * time.Hour)
	var userToday int64
	database.DB.Model(&models.User{}).Where("agent_id = ? AND created_at >= ?", agent.ID, todayStart).Count(&userToday)

	// 今日订单数
	var orderToday int64
	if len(uids) > 0 {
		database.DB.Model(&models.Order{}).Where("user_id IN ? AND created_at >= ?", uids, todayStart).Count(&orderToday)
	}

	// 分润汇总
	var feeShare, lossShare decimal.Decimal
	if len(uids) > 0 {
		database.DB.Model(&models.ProfitShare{}).
			Where("agent_id = ? AND type = 'fee'", agent.ID).
			Select("COALESCE(SUM(amount),0)").Row().Scan(&feeShare)
		database.DB.Model(&models.ProfitShare{}).
			Where("agent_id = ? AND type = 'loss'", agent.ID).
			Select("COALESCE(SUM(amount),0)").Row().Scan(&lossShare)
	}

	// 近7天下单量趋势（统一用 "01-02" 格式）
	type dayCount struct {
		Day string `json:"day"`
		Num int64  `json:"num"`
	}
	now := time.Now()
	// 预填 7 天补零
	trendMap := make(map[string]int64, 7)
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, i-6).Format("01-02")
		trendMap[d] = 0
	}
	if len(uids) > 0 {
		var rows []struct {
			Day string `gorm:"column:day"`
			Num int64  `gorm:"column:num"`
		}
		database.DB.Model(&models.Order{}).
			Select("DATE_FORMAT(created_at, '%m-%d') AS day, COUNT(*) AS num").
			Where("user_id IN ? AND created_at >= ?", uids, now.AddDate(0, 0, -6)).
			Group("day").Find(&rows)
		for _, r := range rows {
			if _, exists := trendMap[r.Day]; exists {
				trendMap[r.Day] = r.Num
			}
		}
	}
	orderTrend := make([]dayCount, 0, 7)
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, i-6).Format("01-02")
		orderTrend = append(orderTrend, dayCount{Day: d, Num: trendMap[d]})
	}

	// 按方向统计订单
	var sideStats []struct {
		Side int   `json:"side"`
		Num  int64 `json:"num"`
	}
	if len(uids) > 0 {
		database.DB.Model(&models.Order{}).
			Select("side, COUNT(*) AS num").
			Where("user_id IN ?", uids).
			Group("side").Find(&sideStats)
	}
	upCount, downCount := int64(0), int64(0)
	for _, s := range sideStats {
		if s.Side == 1 {
			upCount = s.Num
		} else {
			downCount = s.Num
		}
	}

	utils.OK(c, gin.H{
		"userCount":  userCount,
		"userToday":  userToday,
		"orderCount": orderCount,
		"orderToday": orderToday,
		"balance":    agent.Balance,
		"feeShare":   feeShare,
		"lossShare":  lossShare,
		"orderTrend": orderTrend,
		"sideUp":     upCount,
		"sideDown":   downCount,
	})
}

// AgentSubUsers 名下用户列表
func AgentSubUsers(c *gin.Context) {
	agent := middleware.GetAgent(c)
	page, size := utils.Page(c)
	q := database.DB.Model(&models.User{}).Where("agent_id = ?", agent.ID)
	if v := c.Query("keyword"); v != "" {
		like := "%" + v + "%"
		q = q.Where("username LIKE ?", like)
	}
	var total int64
	q.Count(&total)
	var list []models.User
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	for i := range list {
		list[i].Password, list[i].FundPwd = "", "" // 输出层清空敏感哈希
	}
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AgentProfitList 分润流水
func AgentProfitList(c *gin.Context) {
	agent := middleware.GetAgent(c)
	page, size := utils.Page(c)
	q := database.DB.Model(&models.ProfitShare{}).Where("agent_id = ?", agent.ID)
	if v := c.Query("type"); v != "" {
		q = q.Where("type = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.ProfitShare
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AgentApplyWithdraw 代理商申请提现
func AgentApplyWithdraw(c *gin.Context) {
	agent := middleware.GetAgent(c)
	var req struct {
		Amount  string `json:"amount" binding:"required"`
		Account string `json:"account" binding:"required"`
		Name    string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.Sign() <= 0 {
		utils.BadRequest(c, "金额不合法")
		return
	}
	// 快速失败预检（仅提示用；真正并发安全由下方原子条件 UPDATE 保证）
	if agent.Balance.LessThan(amount) {
		utils.BadRequest(c, "可提现余额不足")
		return
	}
	// 原子扣减：balance -= amount, frozen += amount，且要求 balance >= amount
	// （并发下只有一个请求能通过，防超额提现）
	res := database.DB.Exec(
		"UPDATE agents SET balance = balance - ?, frozen_balance = frozen_balance + ? "+
			"WHERE id = ? AND balance >= ?",
		amount, amount, agent.ID, amount)
	if res.Error != nil {
		utils.ServerError(c, "提交失败")
		return
	}
	if res.RowsAffected == 0 {
		utils.BadRequest(c, "可提现余额不足")
		return
	}
	if err := database.DB.Create(&models.AgentWithdraw{
		AgentID: agent.ID, Amount: amount, Account: req.Account, Name: req.Name, Status: 0,
	}).Error; err != nil {
		// 建单失败回滚扣减，避免"余额扣了、冻结悬挂"的幽灵资金
		database.DB.Exec(
			"UPDATE agents SET balance = balance + ?, frozen_balance = frozen_balance - ? WHERE id = ?",
			amount, amount, agent.ID)
		utils.ServerError(c, "提交失败")
		return
	}
	// 代理余额变动：失效代理档案缓存（余额字段）与统计看板
	cache.InvalidateAgent(agent.ID)
	utils.OKMsg(c, "提现申请已提交", nil)
}

// AgentWithdrawList 代理商提现记录
func AgentWithdrawList(c *gin.Context) {
	agent := middleware.GetAgent(c)
	var list []models.AgentWithdraw
	database.DB.Where("agent_id = ?", agent.ID).Order("id desc").Find(&list)
	utils.OK(c, list)
}

// AgentCreditHandler 代理给直属客户上分/下分（type: up上分 / down下分）
func AgentCreditHandler(c *gin.Context) {
	agent := middleware.GetAgent(c)
	var req struct {
		UserID uint   `json:"userId" binding:"required"`
		Amount string `json:"amount" binding:"required"`
		Type   string `json:"type"` // up / down
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.Sign() <= 0 {
		utils.BadRequest(c, "金额不合法")
		return
	}
	remark := req.Remark
	if req.Type == "down" {
		err = trader.AgentDebit(agent.ID, req.UserID, amount, remark)
	} else {
		err = trader.AgentCredit(agent.ID, req.UserID, amount, remark)
	}
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.OKMsg(c, "操作成功", nil)
}

// AgentCreditKinds 代理上分类型提示（国际化友好）
func AgentCreditKinds(c *gin.Context) {
	utils.OK(c, []string{"up_credit", "down_debit"})
}

// AgentSubList 我的下级代理列表
func AgentSubList(c *gin.Context) {
	agent := middleware.GetAgent(c)
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Agent{}).Where("parent_id = ?", agent.ID)
	var total int64
	q.Count(&total)
	var list []models.Agent
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AgentCreateSub 代理创建下级代理，并从自己额度划拨初始额度
func AgentCreateSub(c *gin.Context) {
	parent := middleware.GetAgent(c)
	var req struct {
		AgentName     string          `json:"agentName" binding:"required"`
		Username      string          `json:"username" binding:"required"`
		Password      string          `json:"password" binding:"required"`
		FeeShareRate  decimal.Decimal `json:"feeShareRate"`
		LossShareRate decimal.Decimal `json:"lossShareRate"`
		InitialQuota  string          `json:"initialQuota"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请填写名称/账号/密码")
		return
	}
	if req.FeeShareRate.LessThan(decimal.Zero) || req.FeeShareRate.GreaterThan(decimal.NewFromFloat(1)) ||
		req.LossShareRate.LessThan(decimal.Zero) || req.LossShareRate.GreaterThan(decimal.NewFromFloat(1)) {
		utils.BadRequest(c, "分润比例需在 0-1 之间")
		return
	}
	quota := decimal.Zero
	if req.InitialQuota != "" {
		q, err := decimal.NewFromString(req.InitialQuota)
		if err != nil || q.Sign() < 0 {
			utils.BadRequest(c, "初始额度不合法")
			return
		}
		quota = q
	}
	if quota.GreaterThan(parent.Balance) {
		utils.BadRequest(c, "可用额度不足以划拨")
		return
	}

	var count int64
	database.DB.Model(&models.Agent{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		utils.BadRequest(c, "账号已存在")
		return
	}
	hash, _ := database.Hash(req.Password)
	sub := models.Agent{
		AgentName: req.AgentName, Username: req.Username, Password: hash,
		ParentID: parent.ID, Level: parent.Level + 1,
		FeeShareRate: req.FeeShareRate, LossShareRate: req.LossShareRate,
		Balance: quota, Status: 1,
	}
	if err := database.DB.Create(&sub).Error; err != nil {
		utils.ServerError(c, "创建失败")
		return
	}
	// 从父代理额度划拨：原子条件 UPDATE（WHERE balance >= quota 防并发透支/读改写覆盖）；
	// 划拨失败则回滚已建的子代理，避免「子代理已建、父额度未扣」。
	if quota.Sign() > 0 {
		res := database.DB.Exec(
			"UPDATE agents SET balance = balance - ? WHERE id = ? AND balance >= ?",
			quota, parent.ID, quota)
		if res.Error != nil {
			database.DB.Delete(&models.Agent{}, sub.ID)
			utils.ServerError(c, "额度划拨失败")
			return
		}
		if res.RowsAffected == 0 {
			database.DB.Delete(&models.Agent{}, sub.ID)
			utils.BadRequest(c, "可用额度不足以划拨")
			return
		}
		database.DB.Create(&models.ProfitShare{
			AgentID: parent.ID, Type: "quota_sub", Amount: quota.Neg(), Status: 1,
		})
		database.DB.Create(&models.ProfitShare{
			AgentID: sub.ID, Type: "quota_grant", Amount: quota, Status: 1,
		})
	}
	// 下级代理创建：失效父/子代理档案缓存与统计看板（代理数变更）
	cache.InvalidateAgent(parent.ID)
	cache.InvalidateAgent(sub.ID)
	cache.InvalidateStats()
	utils.OKMsg(c, "下级代理创建成功", sub)
}

// AgentChangePassword 代理商修改密码
func AgentChangePassword(c *gin.Context) {
	agent := middleware.GetAgent(c)
	var req struct {
		OldPassword string `json:"oldPassword" binding:"required"`
		NewPassword string `json:"newPassword" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if !database.CheckPwd(agent.Password, req.OldPassword) {
		utils.BadRequest(c, "原密码错误")
		return
	}
	hash, _ := database.Hash(req.NewPassword)
	database.DB.Model(&models.Agent{}).Where("id = ?", agent.ID).Update("password", hash)
	utils.OKMsg(c, "修改成功", nil)
}

// AdminAgentList 管理端代理列表
func AdminAgentList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Agent{})
	if v := c.Query("keyword"); v != "" {
		q = q.Where("agent_name LIKE ? OR username LIKE ?", "%"+v+"%", "%"+v+"%")
	}
	var total int64
	q.Count(&total)
	var list []models.Agent
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AdminAgentSave 新增/编辑代理商
func AdminAgentSave(c *gin.Context) {
	var req struct {
		ID            uint            `json:"id"`
		AgentName     string          `json:"agentName"`
		Phone         string          `json:"phone"` // 手机号
		Wechat        string          `json:"wechat"`
		QQ            string          `json:"qq"`
		Username      string          `json:"username"`
		Password      string          `json:"password"`
		ParentID      uint            `json:"parentId"`
		FeeShareRate  decimal.Decimal `json:"feeShareRate"`
		LossShareRate decimal.Decimal `json:"lossShareRate"`
		Status        int             `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.ID == 0 {
		var count int64
		database.DB.Model(&models.Agent{}).Where("username = ?", req.Username).Count(&count)
		if count > 0 {
			utils.BadRequest(c, "账号已存在")
			return
		}
		if req.Password == "" {
			utils.BadRequest(c, "请设置密码")
			return
		}
		hash, _ := database.Hash(req.Password)
		agent := models.Agent{
			AgentName: req.AgentName, Phone: req.Phone, Wechat: req.Wechat, QQ: req.QQ,
			Username: req.Username, Password: hash,
			ParentID: req.ParentID, FeeShareRate: req.FeeShareRate,
			LossShareRate: req.LossShareRate, Status: req.Status,
		}
		if agent.Status == 0 {
			agent.Status = 1
		}
		if req.ParentID > 0 {
			var parent models.Agent
			if err := database.DB.First(&parent, req.ParentID).Error; err == nil {
				agent.Level = parent.Level + 1
			} else {
				agent.ParentID = 0
			}
		}
		// 自动生成 6 位字母数字混合邀请码（唯一；冲突重试）
		for tries := 0; tries < 10; tries++ {
			agent.InviteCode = utils.GenInviteCode()
			var dup int64
			database.DB.Model(&models.Agent{}).Where("invite_code = ?", agent.InviteCode).Count(&dup)
			if dup == 0 {
				break
			}
		}
		if err := database.DB.Create(&agent).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		// 代理商新增：失效统计看板（代理数变更）
		cache.InvalidateStats()
		utils.OKMsg(c, "新增成功", agent)
		return
	}
	var a models.Agent
	if err := database.DB.First(&a, req.ID).Error; err != nil {
		utils.BadRequest(c, "代理商不存在")
		return
	}
	updates := map[string]interface{}{
		"agent_name": req.AgentName, "phone": req.Phone, "wechat": req.Wechat, "qq": req.QQ,
		"fee_share_rate": req.FeeShareRate,
		"loss_share_rate": req.LossShareRate, "status": req.Status, "parent_id": req.ParentID,
	}
	if req.Password != "" {
		hash, _ := database.Hash(req.Password)
		updates["password"] = hash
	}
	// 旧数据补邀请码：无邀请码的存量代理在编辑时自动生成
	if a.InviteCode == "" {
		for tries := 0; tries < 10; tries++ {
			code := utils.GenInviteCode()
			var dup int64
			database.DB.Model(&models.Agent{}).Where("invite_code = ?", code).Count(&dup)
			if dup == 0 {
				updates["invite_code"] = code
				break
			}
		}
	}
	if err := database.DB.Model(&models.Agent{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	// 代理商资料/分润率/状态变更：失效档案缓存（禁用语义即时生效）
	cache.InvalidateAgent(req.ID)
	utils.OKMsg(c, "编辑成功", nil)
}

// AdminAgentWithdrawList 管理端代理提现审核列表
func AdminAgentWithdrawList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.AgentWithdraw{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.AgentWithdraw
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AdminAgentWithdrawAudit 代理提现审核
func AdminAgentWithdrawAudit(c *gin.Context) {
	var req struct {
		ID     uint   `json:"id" binding:"required"`
		Status int    `json:"status"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var w models.AgentWithdraw
	if err := database.DB.First(&w, req.ID).Error; err != nil || w.Status != 0 {
		utils.BadRequest(c, "申请不存在或已处理")
		return
	}
	now := time.Now()
	// 原子抢占状态：并发只允许一次审核生效，防双重放款/退款
	res := database.DB.Model(&models.AgentWithdraw{}).Where("id = ? AND status = 0", w.ID).
		Updates(map[string]interface{}{"status": req.Status, "remark": req.Remark, "audit_by": middleware.GetAdminID(c), "audit_at": now})
	if res.Error != nil {
		utils.ServerError(c, "审核失败")
		return
	}
	if res.RowsAffected == 0 {
		utils.BadRequest(c, "申请不存在或已处理")
		return
	}
	// 原子调整代理余额/冻结（读改写 → 原子 SQL，防并发相互覆盖）
	if req.Status == 1 { // 通过：扣减冻结（放款）
		database.DB.Exec("UPDATE agents SET frozen_balance = GREATEST(frozen_balance - ?, 0) WHERE id = ?",
			w.Amount, w.AgentID)
	} else if req.Status == 2 { // 拒绝：解冻并退回余额
		database.DB.Exec("UPDATE agents SET frozen_balance = GREATEST(frozen_balance - ?, 0), balance = balance + ? WHERE id = ?",
			w.Amount, w.Amount, w.AgentID)
	}
	// 代理提现审核：失效代理档案缓存（余额/冻结字段变更）
	cache.InvalidateAgent(w.AgentID)
	utils.OKMsg(c, "审核成功", nil)
}

// AgentOrders 代理商名下(邀请)用户的订单列表
func AgentOrders(c *gin.Context) {
	agent := middleware.GetAgent(c)
	page, size := utils.Page(c)

	var userIDs []uint
	database.DB.Model(&models.User{}).Where("agent_id = ?", agent.ID).Pluck("id", &userIDs)
	if len(userIDs) == 0 {
		utils.OK(c, utils.PageData{List: []interface{}{}, Total: 0, Page: page, Size: size})
		return
	}

	q := database.DB.Model(&models.Order{}).Where("user_id IN ?", userIDs)
	if v := c.Query("userId"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		q = q.Where("order_no LIKE ?", "%"+v+"%")
	}

	var total int64
	q.Count(&total)
	var orders []models.Order
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&orders)

	userName := map[uint]string{}
	prodName := map[uint]string{}
	for _, o := range orders {
		if _, ok := userName[o.UserID]; !ok {
			var u models.User
			if database.DB.Select("username").First(&u, o.UserID).Error == nil {
				userName[o.UserID] = u.Username
			}
		}
		if _, ok := prodName[o.ProductID]; !ok {
			var p models.Product
			if database.DB.Select("name").First(&p, o.ProductID).Error == nil {
				prodName[o.ProductID] = p.Name
			}
		}
	}
	type row struct {
		models.Order
		UserName    string `json:"userName"`
		ProductName string `json:"productName"`
	}
	list := make([]row, 0, len(orders))
	for _, o := range orders {
		list = append(list, row{Order: o, UserName: userName[o.UserID], ProductName: prodName[o.ProductID]})
	}
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AgentPositions 代理商名下(邀请)用户的持仓列表
func AgentPositions(c *gin.Context) {
	agent := middleware.GetAgent(c)
	page, size := utils.Page(c)

	var userIDs []uint
	database.DB.Model(&models.User{}).Where("agent_id = ?", agent.ID).Pluck("id", &userIDs)
	if len(userIDs) == 0 {
		utils.OK(c, utils.PageData{List: []interface{}{}, Total: 0, Page: page, Size: size})
		return
	}

	q := database.DB.Model(&models.Position{}).Where("user_id IN ?", userIDs)
	if v := c.Query("userId"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	} else {
		q = q.Where("status = 1") // 默认只看持仓中
	}
	if v := c.Query("productName"); v != "" {
		var pids []uint
		database.DB.Model(&models.Product{}).Where("name LIKE ?", "%"+v+"%").Pluck("id", &pids)
		if len(pids) > 0 {
			q = q.Where("product_id IN ?", pids)
		} else {
			utils.OK(c, utils.PageData{List: []interface{}{}, Total: 0, Page: page, Size: size})
			return
		}
	}

	var total int64
	q.Count(&total)
	var positions []models.Position
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&positions)

	userName := map[uint]string{}
	prodName := map[uint]string{}
	for _, p := range positions {
		if _, ok := userName[p.UserID]; !ok {
			var u models.User
			if database.DB.Select("username").First(&u, p.UserID).Error == nil {
				userName[p.UserID] = u.Username
			}
		}
		if _, ok := prodName[p.ProductID]; !ok {
			var pr models.Product
			if database.DB.Select("name,last_price").First(&pr, p.ProductID).Error == nil {
				prodName[p.ProductID] = pr.Name
			}
		}
	}
	type row struct {
		models.Position
		UserName    string `json:"userName"`
		ProductName string `json:"productName"`
	}
	list := make([]row, 0, len(positions))
	for _, p := range positions {
		list = append(list, row{Position: p, UserName: userName[p.UserID], ProductName: prodName[p.ProductID]})
	}
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}
