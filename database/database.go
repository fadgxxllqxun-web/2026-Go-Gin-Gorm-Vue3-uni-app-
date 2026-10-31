package database

import (
	"log"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"server/config"
	"server/models"
)

var DB *gorm.DB

func Init() error {
	var err error
	DB, err = gorm.Open(mysql.Open(config.Cfg.DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return err
	}
	// 连接池显式上限：防高并发下单/批量 persist 打爆 MySQL max_connections
	if sqlDB, err := DB.DB(); err == nil {
		sqlDB.SetMaxOpenConns(config.Cfg.Database.MaxOpenConns)
		sqlDB.SetMaxIdleConns(config.Cfg.Database.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(time.Duration(config.Cfg.Database.ConnMaxLifetimeS) * time.Second)
	}

	if err := migrate(); err != nil {
		return err
	}
	seed()
	seedSettings()
	// 幂等补齐出入金渠道开关/实名设置（存量库升级：只补缺、不覆盖）
	ensurePayFundSettings()
	// 幂等补齐邮箱验证开关（默认开启；存量库升级补缺）
	ensureMailVerifySetting()
	// 幂等补齐「交易设置」分组（存量库升级：只补缺、不覆盖），含撮合模式开关 systemMakeup
	ensureTradeSettings()
	ensureAdmin()
	// 幂等补齐交易管理菜单（存量库升级）
	EnsureTradeMenus()
	// 合并历史遗留的重复菜单（见 menu.go），保证登录菜单树无重复
	dedupMenus()
	// 为交易管理子菜单补齐按钮权限（新增/编辑/删除）
	ensureTradeButtons()
	// 补齐 App管理/ Banner管理 菜单及按钮权限
	ensureAppMenus()
	return nil
}

// ensureProfitShareIdempotency 存量库结构兜底（幂等）：
// 为 profit_shares 补建幂等键相关列（action_type/idemp_key）。
// 在 AutoMigrate 之前执行，避免 AutoMigrate 在存量重复数据上建
// uk_profit_share_action 唯一索引失败而中止整个迁移。
// 列类型与 models.ProfitShare 的 GORM 声明保持一致，已存在则跳过。
func ensureProfitShareIdempotency() {
	db := DB
	// 加列（已存在则跳过；幂等）
	for _, ddl := range []string{
		"ALTER TABLE profit_shares ADD COLUMN action_type TINYINT UNSIGNED NOT NULL DEFAULT 0",
		"ALTER TABLE profit_shares ADD COLUMN idemp_key VARCHAR(64) NOT NULL DEFAULT ''",
	} {
		var col string
		if err := db.Raw("SELECT COLUMN_NAME FROM information_schema.columns "+
			"WHERE table_schema = DATABASE() AND table_name = 'profit_shares' AND column_name = ?",
			extractColumnName(ddl)).Scan(&col).Error; err == nil && col != "" {
			continue // 列已存在
		}
		if err := db.Exec(ddl).Error; err != nil {
			log.Printf("[db] ensure profit_shares column err: %v", err)
			// 若列已存在（DDL 报错 Duplicate column），继续
		}
	}
	// 为存量订单分润记录补幂等键/动作类型（不覆盖已有值，幂等）
	// 注意：旧版头寸分润把 product_id 写进了 order_id 列，故 loss 类按 product_id 补键
	if err := db.Exec(
		"UPDATE profit_shares SET action_type = 1, "+
			"idemp_key = CONCAT_WS('|', user_id, type, order_id, 'open') "+
			"WHERE type = 'fee' AND action_type = 0 AND order_id > 0 AND idemp_key = ''",
	).Error; err != nil {
		log.Printf("[db] backfill profit_shares idemp_key (fee/open) err: %v", err)
	}
	if err := db.Exec(
		"UPDATE profit_shares SET action_type = 3, "+
			"idemp_key = CONCAT_WS('|', user_id, type, 'pos', product_id, 'close') "+
			"WHERE type = 'loss' AND action_type = 0 AND product_id > 0 AND idemp_key = ''",
	).Error; err != nil {
		log.Printf("[db] backfill profit_shares idemp_key (loss) err: %v", err)
	}
	// 清理旧逻辑产生的重复幂等键记录（保留每组 id 最小一条），
	// 否则唯一索引在存量数据上建不出来
	if err := db.Exec(
		"DELETE ps FROM profit_shares ps " +
			"JOIN profit_shares keep ON keep.idemp_key = ps.idemp_key " +
			"AND keep.id < ps.id " +
			"WHERE ps.idemp_key <> '' AND ps.status = 1",
	).Error; err != nil {
		log.Printf("[db] dedup profit_shares idemp_key err: %v", err)
	}
}

// extractColumnName 从「ADD COLUMN xxx ...」DDL 中取列名（兜底用）
func extractColumnName(ddl string) string {
	// 形如 "ALTER TABLE profit_shares ADD COLUMN action_type ..."
	const tag = "ADD COLUMN "
	i := strings.Index(ddl, tag)
	if i < 0 {
		return ""
	}
	rest := ddl[i+len(tag):]
	if sp := strings.IndexByte(rest, ' '); sp > 0 {
		return rest[:sp]
	}
	return rest
}

// backfillOpenOrderIDs 为存量平仓单回填 open_order_id（关联的开仓/买入订单ID）。
// 幂等：只处理 open_order_id=0 的平仓单（is_close=1），逐个定位其原开仓单：
//   1) 优先按 position_id 精确关联（撮合回填）；定位不到时
//   2) 兜底取该用户该品种最近一笔已成交(is_close=0,status=2)开仓单。
// 与 services/trader/order.go 的 findSourceOrderID 定位规则保持一致。
func backfillOpenOrderIDs() {
	type closeRow struct {
		ID, UserID, ProductID, PositionID uint
	}
	var closes []closeRow
	DB.Table("orders").Select("id,user_id,product_id,position_id").
		Where("is_close = 1 AND open_order_id = 0").Find(&closes)
	if len(closes) == 0 {
		return
	}
	updated := 0
	for _, c := range closes {
		var oid uint
		if c.PositionID > 0 {
			DB.Table("orders").
				Where("user_id = ? AND position_id = ? AND is_close = 0 AND status = ?", c.UserID, c.PositionID, models.OrderHolding).
				Order("id desc").Limit(1).Pluck("id", &oid)
		}
		if oid == 0 {
			DB.Table("orders").
				Where("user_id = ? AND product_id = ? AND is_close = 0 AND status = ?", c.UserID, c.ProductID, models.OrderHolding).
				Order("id desc").Limit(1).Pluck("id", &oid)
		}
		if oid > 0 {
			if err := DB.Table("orders").Where("id = ?", c.ID).Update("open_order_id", oid).Error; err == nil {
				updated++
			}
		}
	}
	if updated > 0 {
		log.Printf("[db] backfill open_order_id: %d 条平仓单已关联开仓(买入)订单", updated)
	}
}

// migrate 首次 AutoMigrate + 存量库结构兜底。
// profit_shares 幂等键改造（action_type / idemp_key + uk_profit_share_action）
// 在 AutoMigrate 之前先行加列/补键，避免 AutoMigrate 在存量重复数据上建唯一索引失败
// （AutoMigrate 建索引失败会中止整个迁移，导致新字段后续也无法补齐）。
func migrate() error {
	ensureProfitShareIdempotency()
	err := DB.AutoMigrate(
		&models.Admin{},
		&models.Role{},
		&models.RoleMenu{},
		&models.Menu{},
		&models.OperationLog{},
		&models.Setting{},
		// 交易业务表
		&models.User{},
		&models.Wallet{},
		&models.WalletLog{},
		&models.Product{},
		&models.Order{},
		&models.Position{},
		&models.Deposit{},
		&models.Withdraw{},
		&models.PaymentAddress{},
		&models.Agent{},
		&models.AgentWithdraw{},
		&models.ProfitShare{},
		&models.ControlRule{},
		&models.Tick{},
		&models.Kline1min{},
		&models.Notice{},
		&models.Banner{},
	)
	if err != nil {
		// profit_shares 唯一索引在存量重复数据上可能 1062；结构列已由
		// ensureProfitShareIdempotency 补齐，不让该错误中止整个迁移（其余表结构已生效）
		log.Printf("[db] auto-migrate err (profit_shares 唯一索引可稍后补齐): %v", err)
	}
	// 显式补建 uk_profit_share_action 唯一索引（AutoMigrate 因存量重复 1062 中止后兜底；
	// 已存在则静默跳过）
	if idxErr := DB.Exec(
		"CREATE UNIQUE INDEX uk_profit_share_action ON profit_shares (user_id, type, action_type, idemp_key)").Error; idxErr != nil {
		if !strings.Contains(idxErr.Error(), "Duplicate key name") {
			log.Printf("[db] 建 uk_profit_share_action 唯一索引失败（重复数据未清完？稍后人工处理）: %v", idxErr)
		}
	}
	// 为存量平仓单回填 open_order_id（AutoMigrate 已加列；幂等，仅处理缺失值）
	backfillOpenOrderIDs()
	seedProducts()
	log.Println("[db] auto-migrate done")
	return nil
}

// Hash 密码加密
func Hash(pwd string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPwd 校验密码
func CheckPwd(hash, pwd string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pwd)) == nil
}

// seed 初始化基础数据：超管账号 + 角色 + 菜单
func seed() {
	var count int64
	DB.Model(&models.Role{}).Count(&count)
	if count > 0 {
		return
	}

	// 超级角色
	superRole := models.Role{
		Name:    "超级管理员",
		Code:    "super_admin",
		Remark:  "拥有全部权限",
		IsSuper: 1,
		Status:  1,
	}
	if err := DB.Create(&superRole).Error; err != nil {
		log.Println("[seed] create role err:", err)
		return
	}

	// 菜单树：目录 + 菜单
	type menuSeed struct {
		parentID  uint
		name      string
		title     string
		path      string
		component string
		icon      string
		perms     string
		mtype     string
		sort      int
	}
	seeds := []menuSeed{
		{0, "dashboard", "首页", "/home", "home/home", "Odometer", "", "C", 1},
		{0, "system", "系统管理", "/system", "", "Setting", "", "M", 2},
		// 管理员（挂在“系统管理”目录下，目录 ID=2）
		{2, "sysAdmin", "管理员管理", "/system/admin", "system/admin", "User", "sys:admin:list", "C", 3},
		{2, "sysRole", "角色管理", "/system/role", "system/role", "UserFilled", "sys:role:list", "C", 4},
		{2, "sysMenu", "菜单管理", "/system/menu", "system/menu", "Menu", "sys:menu:list", "C", 5},
		{2, "sysPerm", "权限管理", "/system/permission", "system/permission", "Lock", "sys:perm:list", "C", 6},
		{2, "sysLog", "操作日志", "/system/log", "system/log", "Document", "sys:log:list", "C", 7},
		{2, "sysSetting", "系统设置", "/system/setting", "system/setting", "Tools", "sys:setting:list", "C", 8},
	}

	// 按钮权限（挂在对应菜单下：3=管理员 4=角色 5=菜单 6=权限 7=日志 8=设置）
	buttonSeeds := []menuSeed{
		{3, "", "新增", "", "", "", "sys:admin:add", "F", 1},
		{3, "", "编辑", "", "", "", "sys:admin:edit", "F", 2},
		{3, "", "删除", "", "", "", "sys:admin:delete", "F", 3},
		{4, "", "新增", "", "", "", "sys:role:add", "F", 1},
		{4, "", "编辑", "", "", "", "sys:role:edit", "F", 2},
		{4, "", "删除", "", "", "", "sys:role:delete", "F", 3},
		{5, "", "新增", "", "", "", "sys:menu:add", "F", 1},
		{5, "", "编辑", "", "", "", "sys:menu:edit", "F", 2},
		{5, "", "删除", "", "", "", "sys:menu:delete", "F", 3},
		{6, "", "授权", "", "", "", "sys:perm:assign", "F", 1},
		{7, "", "删除", "", "", "", "sys:log:delete", "F", 1},
		{8, "", "保存", "", "", "", "sys:setting:save", "F", 1},
	}

	created := make([]models.Menu, 0)
	for _, s := range seeds {
		m := models.Menu{
			ParentID: s.parentID, Name: s.name, Title: s.title, Path: s.path,
			Component: s.component, Icon: s.icon, Perms: s.perms,
			Type: s.mtype, Sort: s.sort, Visible: 1, Status: 1,
		}
		if err := DB.Create(&m).Error; err != nil {
			log.Println("[seed] menu err:", err)
		}
		created = append(created, m)
	}
	for _, s := range buttonSeeds {
		m := models.Menu{
			ParentID: s.parentID, Name: s.name, Title: s.title, Path: s.path,
			Component: s.component, Icon: s.icon, Perms: s.perms,
			Type: s.mtype, Sort: s.sort, Visible: 1, Status: 1,
		}
		if err := DB.Create(&m).Error; err != nil {
			log.Println("[seed] button err:", err)
		}
		created = append(created, m)
	}

	log.Printf("[seed] seeded: %d menus, super admin/role created", len(created))
}

// ensureAdmin 确保存在默认超级管理员账号
func ensureAdmin() {
	var superRole models.Role
	if err := DB.Where("is_super = 1").First(&superRole).Error; err != nil {
		return
	}
	var count int64
	DB.Model(&models.Admin{}).Where("username = ?", "admin").Count(&count)
	if count > 0 {
		return
	}
	pwd, _ := Hash("123456")
	admin := models.Admin{
		Username: "admin",
		Password: pwd,
		Nickname: "超级管理员",
		Email:    "admin@gox.com",
		RoleID:   superRole.ID,
		Status:   1,
	}
	if err := DB.Create(&admin).Error; err != nil {
		log.Println("[seed] create admin err:", err)
		return
	}
	log.Println("[seed] default admin created (admin/123456)")
}

// seedProducts 初始化默认交易品种
func seedProducts() {
	var count int64
	DB.Model(&models.Product{}).Count(&count)
	if count > 0 {
		return
	}
	products := []models.Product{
		{Name: "咖啡豆", Code: "COFFEE", TickSize: decimal.NewFromFloat(0.01), MarginRate: decimal.NewFromFloat(0.002), UnitValue: decimal.NewFromInt(100), Leverage: 10, FeePerHand: decimal.NewFromFloat(0.5), FeeRate: decimal.Zero, MinVolume: 1, MaxVolume: 100, MaxPosition: 500, LastPrice: decimal.NewFromFloat(21.50), OpenPrice: decimal.NewFromFloat(21.50), Status: 1, Sort: 1},
		{Name: "美元指数", Code: "USDX", TickSize: decimal.NewFromFloat(0.001), MarginRate: decimal.NewFromFloat(0.002), UnitValue: decimal.NewFromInt(100), Leverage: 10, FeePerHand: decimal.NewFromFloat(0.5), FeeRate: decimal.Zero, MinVolume: 1, MaxVolume: 100, MaxPosition: 500, LastPrice: decimal.NewFromFloat(103.650), OpenPrice: decimal.NewFromFloat(103.650), Status: 1, Sort: 2},
		{Name: "伦敦金", Code: "XAU", TickSize: decimal.NewFromFloat(0.10), MarginRate: decimal.NewFromFloat(0.002), UnitValue: decimal.NewFromInt(100), Leverage: 10, FeePerHand: decimal.NewFromFloat(2.0), FeeRate: decimal.Zero, MinVolume: 1, MaxVolume: 100, MaxPosition: 500, LastPrice: decimal.NewFromFloat(2020.55), OpenPrice: decimal.NewFromFloat(2020.55), Status: 1, Sort: 3},
	}
	for _, p := range products {
		DB.Create(&p)
	}
	log.Println("[seed] default products seeded")
}

// seedSettings 初始化系统设置默认值
func seedSettings() {
	// 推广链接前缀等【后新增】配置项：对已初始化的库也要幂等补插
	// （seedSettings 整表非空时下面的 count>0 会提前返回，故此处单独 FirstOrCreate）。
	DB.Where(models.Setting{Key: "clientSiteUrl"}).FirstOrCreate(&models.Setting{
		GroupCode: "website", GroupName: "网站配置", Key: "clientSiteUrl",
		Value: "https://jsadmin.dachengfx.com",
		Description: "推广网址：代理商推广链接前缀（含协议与域名，如 https://jsadmin.dachengfx.com）",
	})

	var count int64
	DB.Model(&models.Setting{}).Count(&count)
	if count > 0 {
		return
	}
	defaults := []models.Setting{
		{GroupCode: "website", GroupName: "网站设置", Key: "siteName", Value: "ease-ui 管理系统", Description: "网站名称"},
		{GroupCode: "website", GroupName: "网站设置", Key: "siteLogo", Value: "", Description: "网站 Logo"},
		{GroupCode: "website", GroupName: "网站设置", Key: "siteUrl", Value: "http://localhost:3000", Description: "网站地址"},
		{GroupCode: "website", GroupName: "网站设置", Key: "icp", Value: "", Description: "备案号"},
		{GroupCode: "website", GroupName: "网站设置", Key: "copyright", Value: "Copyright © 2026 ease-ui", Description: "版权信息"},
		{GroupCode: "website", GroupName: "网站设置", Key: "version", Value: "v1.0.0", Description: "系统版本"},

		{GroupCode: "oss", GroupName: "OSS 存储", Key: "endpoint", Value: "", Description: "Endpoint"},
		{GroupCode: "oss", GroupName: "OSS 存储", Key: "bucket", Value: "", Description: "Bucket"},
		{GroupCode: "oss", GroupName: "OSS 存储", Key: "accessKeyId", Value: "", Description: "AccessKey ID"},
		{GroupCode: "oss", GroupName: "OSS 存储", Key: "accessKeySecret", Value: "", Description: "AccessKey Secret"},
		{GroupCode: "oss", GroupName: "OSS 存储", Key: "domain", Value: "", Description: "访问域名"},

		{GroupCode: "email", GroupName: "邮箱设置", Key: "host", Value: "", Description: "SMTP 主机"},
		{GroupCode: "email", GroupName: "邮箱设置", Key: "port", Value: "465", Description: "SMTP 端口"},
		{GroupCode: "email", GroupName: "邮箱设置", Key: "username", Value: "", Description: "邮箱账号"},
		{GroupCode: "email", GroupName: "邮箱设置", Key: "password", Value: "", Description: "授权码"},
		{GroupCode: "email", GroupName: "邮箱设置", Key: "from", Value: "", Description: "发件人"},
		{GroupCode: "email", GroupName: "邮箱设置", Key: "ssl", Value: "true", Description: "SSL 加密"},
		{GroupCode: "email", GroupName: "邮箱设置", Key: "mailVerify", Value: "1", Description: "注册是否需要邮箱验证码（1开启 0关闭）"},

		{GroupCode: "sms", GroupName: "短信设置", Key: "accessKeyId", Value: "", Description: "AccessKey ID"},
		{GroupCode: "sms", GroupName: "短信设置", Key: "accessKeySecret", Value: "", Description: "AccessKey Secret"},
		{GroupCode: "sms", GroupName: "短信设置", Key: "signName", Value: "", Description: "短信签名"},
		{GroupCode: "sms", GroupName: "短信设置", Key: "templateCode", Value: "", Description: "模板编码"},
	}
	defaults = append(defaults, payFundSettingDefaults()...)
	for _, s := range defaults {
		DB.Create(&s)
	}
	log.Println("[seed] system settings seeded")
}

// payFundSettingDefaults 出入金相关设置默认值：
// 充值/出金各渠道开关 + 是否强制实名。
func payFundSettingDefaults() []models.Setting {
	return []models.Setting{
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "depositBank", Value: "1", Description: "充值-银行卡 是否开启"},
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "depositAlipay", Value: "1", Description: "充值-支付宝 是否开启"},
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "depositUsdt", Value: "1", Description: "充值-USDT 是否开启"},
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "withdrawBank", Value: "1", Description: "出金-银行卡 是否开启"},
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "withdrawAlipay", Value: "1", Description: "出金-支付宝 是否开启"},
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "withdrawUsdt", Value: "1", Description: "出金-USDT 是否开启"},
		{GroupCode: "payfund", GroupName: "出入金设置", Key: "needRealName", Value: "1", Description: "是否需要实名认证（0关闭=注册即默认已实名）"},
	}
}

// ensurePayFundSettings 幂等补齐出入金设置：存量库升级时补缺、不覆盖已改配置。
func ensurePayFundSettings() {
	for _, s := range payFundSettingDefaults() {
		var cnt int64
		DB.Model(&models.Setting{}).Where("`key` = ?", s.Key).Count(&cnt)
		if cnt == 0 {
			DB.Create(&s)
		}
	}
}

// ensureMailVerifySetting 幂等补齐「邮箱验证码开关」（邮箱设置组，默认开启）。
func ensureMailVerifySetting() {
	var cnt int64
	DB.Model(&models.Setting{}).Where("group_code = ? AND `key` = ?", "email", "mailVerify").Count(&cnt)
	if cnt == 0 {
		DB.Create(&models.Setting{GroupCode: "email", GroupName: "邮箱设置", Key: "mailVerify", Value: "1", Description: "注册是否需要邮箱验证码（1开启 0关闭）"})
	}
}

// ensureTradeSettings 幂等补齐「交易设置」分组：存量库升级只补缺、不覆盖已改配置。
// 目前含：systemMakeup 系统兜底开关（默认开启=系统承接；关闭=用户之间价格优先撮合）。
func ensureTradeSettings() {
	var cnt int64
	DB.Model(&models.Setting{}).Where("group_code = ? AND `key` = ?", "trade", "systemMakeup").Count(&cnt)
	if cnt == 0 {
		DB.Create(&models.Setting{
			GroupCode:   "trade",
			GroupName:   "交易设置",
			Key:         "systemMakeup",
			Value:       "1",
			Description: "是否开启系统兜底（1开启=系统承接对手盘/B-Book；0关闭=用户之间价格优先撮合/A-Book）",
		})
	}
}