package routers

import (
	"os"
	"runtime"

	"github.com/gin-gonic/gin"

	"server/config"
	"server/controllers"
	"server/middleware"
	"server/services/mktws"
	"server/services/push"
)

// InitRouter 初始化路由
func InitRouter() *gin.Engine {
	r := gin.Default()
	if config.Cfg.Cors.Enabled {
		r.Use(middleware.CORS())
	}
	// 访问有效期控制：到期后禁止正常访问（先于业务中间件，最后一次生效）
	r.Use(middleware.AccessGuard())
	r.Use(middleware.OperationLog())

	// 静态资源（上传文件）
	_ = os.MkdirAll(config.Cfg.Upload.Dir, 0o755)
	r.Static("/static", config.Cfg.Upload.Dir)

	api := r.Group("/api")

	// ---- 认证（公开） ----
	auth := api.Group("/auth")
	{
		auth.POST("/login", controllers.Login)
	}

	// ---- 公开信息（无需登录，供登录页/品牌区读取） ----
	api.GET("/public/site-info", controllers.PublicSiteInfo)
	// 公开 banner 列表（供 C 端 App 首页轮播，无需登录）
	api.GET("/public/banners", controllers.PublicBanners)

	// ---- 以下均需登录 ----
	authed := api.Group("")
	authed.Use(middleware.Auth())
	{
		authed.GET("/auth/info", controllers.Info)
		authed.GET("/auth/menus", controllers.Menus)
		authed.POST("/auth/logout", controllers.Logout)
		authed.POST("/auth/change-password", controllers.ChangePassword)
		authed.POST("/auth/profile", controllers.ProfileUpdate)

		// 首页统计
		authed.GET("/dashboard/stats", controllers.Dashboard)

		// 顶部消息通知
		authed.GET("/notify/latest", controllers.AdminLatestEvents)

		// 文件上传（需登录）
		authed.POST("/upload", controllers.Upload)

		// ---- 交易管理后台 ----
		authed.GET("/trade/dashboard", middleware.Perm("trade:dashboard"), controllers.AdminTradeDashboard)
		authed.GET("/trade/products", middleware.Perm("trade:product"), controllers.AdminProductList)
		authed.POST("/trade/product/save", middleware.PermAny("trade:product:add", "trade:product:edit"), controllers.ProductSave)
		authed.PUT("/trade/product/status", middleware.Perm("trade:product:edit"), controllers.ProductStatus)
		authed.DELETE("/trade/product/:id", middleware.Perm("trade:product:delete"), controllers.ProductDelete)
		authed.GET("/trade/users", middleware.Perm("trade:user"), controllers.AdminUserList)
		authed.PUT("/trade/user/status", middleware.Perm("trade:user:edit"), controllers.AdminUserStatus)
		authed.POST("/trade/user/update", middleware.PermAny("trade:user:add", "trade:user:edit"), controllers.AdminUserUpdate)
		authed.POST("/trade/user/realname-audit", middleware.Perm("trade:user:edit"), controllers.AdminRealNameAudit)
		authed.DELETE("/trade/user/:id", middleware.Perm("trade:user:delete"), controllers.AdminUserDelete)
		authed.POST("/trade/user/force-close", middleware.Perm("trade:user:edit"), controllers.AdminForceCloseUser)
		authed.POST("/trade/user/balance", middleware.Perm("trade:user:edit"), controllers.EditBalance)
		authed.GET("/trade/orders", middleware.Perm("trade:order"), controllers.AdminOrderList)
		authed.GET("/trade/positions", middleware.Perm("trade:position"), controllers.AdminPositionList)
		authed.GET("/trade/deposits", middleware.Perm("trade:deposit"), controllers.DepositList)
		authed.POST("/trade/deposit/audit", middleware.Perm("trade:deposit:edit"), controllers.DepositAudit)
		authed.GET("/trade/withdraws", middleware.Perm("trade:withdraw"), controllers.WithdrawList)
		authed.POST("/trade/withdraw/audit", middleware.Perm("trade:withdraw:edit"), controllers.WithdrawAudit)
		authed.GET("/trade/wallet-logs", middleware.Perm("trade:flow"), controllers.AdminWalletLogList)
		authed.POST("/trade/control/save", middleware.PermAny("trade:control:add", "trade:control:edit"), controllers.CreateControlRule)
		authed.GET("/trade/control/list", middleware.Perm("trade:control"), controllers.ControlRuleList)
		authed.POST("/trade/control/strategy", middleware.Perm("trade:control:edit"), controllers.AdminControlStrategy)
		authed.POST("/trade/close-all", middleware.Perm("trade:control:edit"), controllers.CloseAllPositions)
		// 行情控制 / 平台盈亏
		authed.GET("/trade/market/stats", middleware.Perm("trade:control"), controllers.AdminMarketStats)
		authed.POST("/trade/market/freeze", middleware.Perm("trade:control:edit"), controllers.AdminMarketFreeze)
		authed.POST("/trade/market/mode", middleware.Perm("trade:control:edit"), controllers.AdminMarketMode)
		authed.POST("/trade/market/hours", middleware.Perm("trade:control:edit"), controllers.AdminMarketHours)
		// 交易设置：系统强平线
		authed.GET("/trade/settings", middleware.Perm("trade:control"), controllers.AdminTradeSettings)
		authed.POST("/trade/strong-close", middleware.Perm("trade:control:edit"), controllers.AdminStrongClose)
		authed.POST("/trade/inject", middleware.Perm("trade:control:edit"), controllers.AdminInjectOrder)
		authed.POST("/trade/inject-clear", middleware.Perm("trade:control:edit"), controllers.AdminClearInject)
		authed.GET("/trade/agent-trans", middleware.Perm("trade:agent"), controllers.AdminAgentTrans)
		authed.POST("/trade/agent/balance", middleware.Perm("trade:agent:edit"), controllers.AdminAgentAdjust)
		// 公告管理
		authed.GET("/notice/list", middleware.Perm("trade:notice"), controllers.AdminNoticeList)
		authed.POST("/notice/create", middleware.PermAny("trade:notice:add", "trade:notice:edit"), controllers.AdminNoticeCreate)
		authed.DELETE("/notice/:id", middleware.Perm("trade:notice:delete"), controllers.AdminNoticeDelete)
		// App管理：Banner 轮播图
		authed.GET("/banner/list", middleware.Perm("app:banner"), controllers.AdminBannerList)
		authed.POST("/banner/save", middleware.PermAny("app:banner:add", "app:banner:edit"), controllers.AdminBannerSave)
		authed.PUT("/banner/status", middleware.Perm("app:banner:edit"), controllers.AdminBannerStatus)
		authed.DELETE("/banner/:id", middleware.Perm("app:banner:delete"), controllers.AdminBannerDelete)
		// 代理商管理
		authed.GET("/trade/agents", middleware.Perm("trade:agent"), controllers.AdminAgentList)
		authed.POST("/trade/agent/save", middleware.PermAny("trade:agent:add", "trade:agent:edit"), controllers.AdminAgentSave)
		authed.DELETE("/trade/agent/:id", middleware.Perm("trade:agent:delete"), controllers.AdminAgentDelete)
		authed.GET("/trade/agent-withdraws", middleware.Perm("trade:agent"), controllers.AdminAgentWithdrawList)
		authed.POST("/trade/agent-withdraw/audit", middleware.Perm("trade:agent:edit"), controllers.AdminAgentWithdrawAudit)
		// 收款地址管理（支付宝/银行卡/USDT）
		authed.GET("/trade/payment-addresses", middleware.Perm("trade:payaddress"), controllers.PaymentAddressList)
		authed.GET("/trade/payment-addresses/all", middleware.Perm("trade:payaddress"), controllers.PaymentAddressAll)
		authed.POST("/trade/payment-address/save", middleware.PermAny("trade:payaddress:add", "trade:payaddress:edit"), controllers.PaymentAddressSave)
		authed.DELETE("/trade/payment-address/:id", middleware.Perm("trade:payaddress:delete"), controllers.PaymentAddressDelete)
		authed.POST("/trade/payment-address/reset", middleware.Perm("trade:payaddress:edit"), controllers.PaymentAddressResetStats)
	}

	// ---- 管理员管理 ----
	admin := api.Group("/admin")
	admin.Use(middleware.Auth())
	{
		admin.GET("/list", middleware.Perm("sys:admin:list"), controllers.AdminList)
		admin.POST("/save", middleware.Perm("sys:admin:add"), controllers.AdminSave)
		admin.PUT("/status", middleware.Perm("sys:admin:edit"), controllers.AdminStatus)
		admin.DELETE("/:id", middleware.Perm("sys:admin:delete"), controllers.AdminDelete)
		admin.GET("/all", middleware.Perm("sys:admin:list"), controllers.AdminAll)
	}

	// ---- 角色管理 ----
	role := api.Group("/role")
	role.Use(middleware.Auth())
	{
		role.GET("/list", middleware.Perm("sys:role:list"), controllers.RoleList)
		role.POST("/save", middleware.Perm("sys:role:add"), controllers.RoleSave)
		role.PUT("/status", middleware.Perm("sys:role:edit"), controllers.RoleStatus)
		role.DELETE("/:id", middleware.Perm("sys:role:delete"), controllers.RoleDelete)
		role.GET("/all", middleware.Perm("sys:role:list"), controllers.RoleAll)
		role.POST("/assign-menus", middleware.Perm("sys:perm:assign"), controllers.RoleAssignMenus)
		role.GET("/:id/menus", middleware.Perm("sys:perm:assign"), controllers.RoleMenus)
	}

	// ---- 菜单管理 ----
	menu := api.Group("/menu")
	menu.Use(middleware.Auth())
	{
		menu.GET("/tree", middleware.Perm("sys:menu:list"), controllers.MenuTree)
		menu.POST("/save", middleware.Perm("sys:menu:add"), controllers.MenuSave)
		menu.PUT("/status", middleware.Perm("sys:menu:edit"), controllers.MenuStatus)
		menu.POST("/sort", middleware.Perm("sys:menu:edit"), controllers.MenuSort)
		menu.DELETE("/:id", middleware.Perm("sys:menu:delete"), controllers.MenuDelete)
	}

	// ---- 权限管理 ----
	perm := api.Group("/permission")
	perm.Use(middleware.Auth())
	{
		perm.GET("/tree", middleware.Perm("sys:perm:list"), controllers.PermTree)
		perm.GET("/list", middleware.Perm("sys:perm:list"), controllers.PermList)
		perm.POST("/assign", middleware.Perm("sys:perm:assign"), controllers.PermAssign)
	}

	// ---- 操作日志 ----
	logg := api.Group("/log")
	logg.Use(middleware.Auth())
	{
		logg.GET("/list", middleware.Perm("sys:log:list"), controllers.LogList)
		logg.GET("/modules", middleware.Perm("sys:log:list"), controllers.LogModules)
		logg.DELETE("/:id", middleware.Perm("sys:log:delete"), controllers.LogDelete)
		logg.DELETE("/clear", middleware.Perm("sys:log:delete"), controllers.LogClear)
	}

	// ---- 系统设置 ----
	setting := api.Group("/setting")
	setting.Use(middleware.Auth())
	{
		setting.GET("", middleware.Perm("sys:setting:list"), controllers.Settings)
		setting.PUT("/save", middleware.Perm("sys:setting:save"), controllers.SettingSave)
		setting.POST("/test-email", middleware.Perm("sys:setting:save"), controllers.TestEmail)
	}

	// ---- C端用户（用户端 App/H5/PC） ----
	uat := api.Group("/user")
	{
		uat.POST("/register", controllers.UserRegister)
		uat.POST("/login", controllers.UserLogin)
	}
	userAuthed := uat.Group("")
	userAuthed.Use(middleware.UserAuth())
	{
		userAuthed.GET("/info", controllers.UserInfo)
		userAuthed.POST("/change-password", controllers.UserChangePassword)
		userAuthed.POST("/fund-password", controllers.SetFundPassword)
		userAuthed.GET("/wallet", controllers.MyWallet)
		userAuthed.POST("/verify", controllers.UserVerify)
		userAuthed.GET("/notices", controllers.UserNotices)
		userAuthed.GET("/notices/unread", controllers.UserNoticeUnread)
		userAuthed.POST("/notices/read", controllers.UserNoticeRead)
		userAuthed.GET("/wallet/logs", controllers.WalletLogs)
		userAuthed.POST("/deposit", controllers.ApplyDeposit)
		userAuthed.POST("/withdraw", controllers.ApplyWithdraw)
		userAuthed.POST("/order", controllers.DoOpenOrder)
		userAuthed.DELETE("/order/:id", controllers.CancelOrder)
		userAuthed.GET("/orders", controllers.MyOrders)
		userAuthed.GET("/positions", controllers.MyPositions)
		userAuthed.POST("/position/close", controllers.PositionClose)
	}

	// ---- 公开行情 ----
	pubTrade := api.Group("/market")
	{
		pubTrade.GET("/products", controllers.ProductList)
		pubTrade.GET("/kline", controllers.KlineList)
		pubTrade.GET("/predict", controllers.PredictK)
		pubTrade.GET("/book", controllers.MarketBook)
		pubTrade.GET("/time", controllers.MarketTime)
	}

	// ---- 代理商后台 ----
	agt := api.Group("/agent")
	{
		agt.POST("/login", controllers.AgentLogin)
	}
	agentAuthed := agt.Group("")
	agentAuthed.Use(middleware.AgentAuth())
	{
		agentAuthed.GET("/info", controllers.AgentInfo)
		agentAuthed.GET("/promo-base", controllers.AgentPromoBase)
		agentAuthed.GET("/dashboard", controllers.AgentDashboard)
		agentAuthed.GET("/users", controllers.AgentSubUsers)
		agentAuthed.GET("/orders", controllers.AgentOrders)
		agentAuthed.GET("/positions", controllers.AgentPositions)
		agentAuthed.GET("/profits", controllers.AgentProfitList)
		agentAuthed.POST("/withdraw", controllers.AgentApplyWithdraw)
		agentAuthed.GET("/withdraws", controllers.AgentWithdrawList)
		agentAuthed.POST("/change-password", controllers.AgentChangePassword)
		agentAuthed.POST("/credit", controllers.AgentCreditHandler)
		agentAuthed.GET("/subs", controllers.AgentSubList)
		agentAuthed.POST("/sub-create", controllers.AgentCreateSub)
	}

	// ---- WebSocket 推送 ----
	// 连接方式: /ws?role=user&user_id=1  或 /ws?role=admin
	r.GET("/ws", func(c *gin.Context) {
		push.GetHub().ServeWS(c)
	})
	// 交易客户端行情频道网关
	r.GET("/ws/market", func(c *gin.Context) {
		mktws.ServeWS(c)
	})

	// ---- 交易客户端兼容层 /trader/*（前端web协议：code:0 / x-token） ----
	trd := api.Group("/trader")
	{
		trd.POST("/auth/sms", controllers.TraderSendSms)
		trd.POST("/auth/mail-code", controllers.TraderSendMailCode)
		trd.POST("/auth/register", controllers.TraderRegister)
		trd.POST("/auth/login", controllers.TraderLogin)
		trd.GET("/market/products", controllers.TraderProducts)
		trd.GET("/market/ticker", controllers.TraderTicker)
		trd.GET("/market/klines", controllers.TraderKlines)
		trd.GET("/market/depth", controllers.TraderDepth)
		trd.GET("/market/trades", controllers.TraderRecentTrades)
		trd.GET("/market/time", controllers.TraderTime)

		trd.GET("/user/info", controllers.TraderUserInfo)
		trd.POST("/user/profile", controllers.TraderUpdateProfile)
		trd.POST("/user/password", controllers.TraderChangePassword)
		trd.POST("/user/fundPassword", controllers.TraderFundPassword)
		trd.POST("/user/realname", controllers.TraderRealname)
		trd.GET("/user/agentContact", controllers.TraderAgentContact)
		trd.GET("/notice/list", controllers.TraderNotices)
		trd.POST("/notice/read", controllers.TraderReadNotice)

		trd.POST("/order/place", controllers.TraderPlaceOrder)
		trd.GET("/order/estimate", controllers.TraderOrderEstimate)
		trd.POST("/order/cancel", controllers.TraderCancelOrder)
		trd.GET("/order/current", controllers.TraderCurrentOrders)
		trd.GET("/order/history", controllers.TraderHistoryOrders)
		trd.GET("/trades", controllers.TraderMyTrades)
		trd.GET("/positions", controllers.TraderPositions)
		trd.POST("/position/close", controllers.TraderClosePosition)
		trd.POST("/position/closeAll", controllers.TraderCloseAll)
		trd.POST("/position/setTPSL", controllers.TraderSetTPSL)

		trd.GET("/asset/overview", controllers.TraderAsset)
		trd.GET("/asset/fundLogs", controllers.TraderFundLogs)
		trd.POST("/withdraw/apply", controllers.TraderApplyWithdraw)
		trd.POST("/deposit/apply", controllers.TraderApplyDeposit)
		trd.GET("/deposit/list", controllers.TraderDepositList)
		trd.GET("/withdraw/list", controllers.TraderWithdrawList)
		trd.POST("/upload", controllers.TraderUpload)
		// 出入金：渠道配置 + 收款地址轮询配发
		trd.GET("/payment-config", controllers.TraderPaymentConfig)
		trd.GET("/payment-address", controllers.TraderPaymentAddress)
	}

	// 健康检查
	r.GET("/api/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 200, "message": "pong"})
	})

	// 调试：dump 所有 goroutine 堆栈（排查死锁）。需登录，避免生产裸奔暴露堆栈信息
	r.GET("/debug/goroutines", middleware.Auth(), func(c *gin.Context) {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		c.String(200, string(buf[:n]))
	})

	return r
}
