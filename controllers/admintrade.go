package controllers

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"server/cache"
	"server/database"
	"server/middleware"
	"server/models"
	"server/services/market"
	"server/services/notify"
	"server/services/trader"
	"server/utils"
)

// AdminTradeDashboard 交易数据看板（8 次聚合走 15s 短缓存，减少看板轮询对 DB 的压力）
func AdminTradeDashboard(c *gin.Context) {
	stats, err := cache.StatTrade(func() (*cache.TradeDashboard, error) {
		var userCount, orderTotal, orderToday int64
		database.DB.Model(&models.User{}).Count(&userCount)
		database.DB.Model(&models.Order{}).Count(&orderTotal)
		startDay := time.Now().Truncate(24 * time.Hour)
		database.DB.Model(&models.Order{}).Where("created_at >= ?", startDay).Count(&orderToday)

		var posVolume int
		database.DB.Model(&models.Position{}).
			Where("status = 1").Select("COALESCE(SUM(volume),0)").Row().Scan(&posVolume)

		var depositTotal decimal.Decimal
		database.DB.Model(&models.Deposit{}).Where("status = 1").
			Select("COALESCE(SUM(amount),0)").Row().Scan(&depositTotal)

		var agentCount int64
		database.DB.Model(&models.Agent{}).Count(&agentCount)

		var depositPending, withdrawPending, agentWithdrawPending, realnamePending int64
		database.DB.Model(&models.Deposit{}).Where("status = 0").Count(&depositPending)
		database.DB.Model(&models.Withdraw{}).Where("status = 0").Count(&withdrawPending)
		database.DB.Model(&models.AgentWithdraw{}).Where("status = 0").Count(&agentWithdrawPending)
		// 待审核实名认证：real_name_status = 1 (0未提交 1审核中 2已通过 3已驳回)
		database.DB.Model(&models.User{}).Where("real_name_status = 1").Count(&realnamePending)

		var recentOrders []models.Order
		database.DB.Order("id desc").Limit(10).Find(&recentOrders)

		return &cache.TradeDashboard{
			UserCount: userCount, OrderTotal: orderTotal, OrderToday: orderToday,
			PositionVolume: posVolume, DepositTotal: depositTotal.String(), AgentCount: agentCount,
			DepositPending: depositPending, WithdrawPending: withdrawPending, AgentWdPending: agentWithdrawPending,
			RealNamePending: realnamePending, RecentOrders: recentOrders,
		}, nil
	})
	if err != nil {
		utils.ServerError(c, "加载看板失败")
		return
	}
	utils.OK(c, gin.H{
		"stats": gin.H{
			"userCount": stats.UserCount, "orderTotal": stats.OrderTotal, "orderToday": stats.OrderToday,
			"positionVolume": stats.PositionVolume, "depositTotal": stats.DepositTotal, "agentCount": stats.AgentCount,
		},
		"pending": gin.H{
			"deposit": stats.DepositPending, "withdraw": stats.WithdrawPending,
			"agentWithdraw": stats.AgentWdPending, "realname": stats.RealNamePending,
		},
		"recentOrders": stats.RecentOrders,
	})
}

// AdminProductList 管理端品种列表
func AdminProductList(c *gin.Context) {
	var list []models.Product
	database.DB.Order("sort asc").Find(&list)
	utils.OK(c, list)
}

// ProductSave 新增/编辑品种
func ProductSave(c *gin.Context) {
	var req models.Product
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.ID == 0 {
		if req.UnitValue.IsZero() {
			req.UnitValue = decimal.NewFromInt(1)
		}
		if req.Status == 0 {
			req.Status = 1
		}
		if req.Leverage <= 0 {
			req.Leverage = 10 // 新增品种默认杠杆 10
		}
		if err := database.DB.Create(&req).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		// 品种变更：失效品种缓存（单条/列表/按code）与统计看板
		cache.InvalidateProducts(req.ID)
		trader.InvalidateProductParam(req.ID)
		cache.InvalidateProductCode(req.Code)
		cache.InvalidateStats()
		utils.OKMsg(c, "新增成功", req)
		return
	}
	var p models.Product
	if err := database.DB.First(&p, req.ID).Error; err != nil {
		utils.BadRequest(c, "品种不存在")
		return
	}
	updates := map[string]interface{}{
		"name": req.Name, "code": req.Code, "tick_size": req.TickSize,
		"margin_rate": req.MarginRate, "fixed_margin": req.FixedMargin, "unit_value": req.UnitValue,
		"fee_per_hand": req.FeePerHand, "fee_rate": req.FeeRate,
		"float_step": req.FloatStep, "day_float_rate": req.DayFloatRate,
		"wick_length": req.WickLength,
		"min_volume":     req.MinVolume, "max_volume": req.MaxVolume,
		"max_position": req.MaxPosition, "status": req.Status, "sort": req.Sort,
		"leverage":       req.Leverage,
		"anti_flat":      req.AntiFlat,
	}
	if req.Leverage <= 0 {
		updates["leverage"] = 10 // 兜底默认杠杆
	}
	if err := database.DB.Model(&models.Product{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	// 品种编辑：code 可能变更，同时失效旧 code 缓存与新 code 缓存
	if p.Code != req.Code {
		cache.InvalidateProductCode(p.Code)
	}
	cache.InvalidateProducts(req.ID)
	trader.InvalidateProductParam(req.ID)
	cache.InvalidateProductCode(req.Code)
	cache.InvalidateStats()
	utils.OKMsg(c, "编辑成功", nil)
}

// ProductStatus 启用/停盘
func ProductStatus(c *gin.Context) {
	var req struct {
		ID     uint `json:"id" binding:"required"`
		Status int  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	database.DB.Model(&models.Product{}).Where("id = ?", req.ID).Update("status", req.Status)
	// 上下架：品种列表缓存（status=1 列表）需失效
	cache.InvalidateProducts(req.ID)
	trader.InvalidateProductParam(req.ID)
	utils.OKMsg(c, "操作成功", nil)
}

// AdminUserList 管理端C端用户列表
func AdminUserList(c *gin.Context) {
	page, size := utils.Page(c)
	keyword := c.Query("keyword")
	status := c.Query("status")
	q := database.DB.Model(&models.User{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("username LIKE ? OR phone LIKE ? OR nickname LIKE ?", like, like, like)
	}
	if status != "" {
		q = q.Where("status = ?", status)
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

// AdminUserStatus 启用/禁用C端用户
func AdminUserStatus(c *gin.Context) {
	var req struct {
		ID     uint `json:"id" binding:"required"`
		Status int  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	database.DB.Model(&models.User{}).Where("id = ?", req.ID).Update("status", req.Status)
	// 禁号必须即时失效用户档案缓存（安全红线）
	cache.InvalidateUser(req.ID)
	utils.OKMsg(c, "操作成功", nil)
}

// EditBalance 手动资金调整
func EditBalance(c *gin.Context) {
	var req struct {
		UserID uint   `json:"userId" binding:"required"`
		Amount string `json:"amount" binding:"required"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.IsZero() {
		utils.BadRequest(c, "金额不合法")
		return
	}
	adminID := middleware.GetAdminID(c)
	remark := req.Remark
	if amount.Sign() > 0 {
		err = trader.Credit(req.UserID, amount, "adjust", remark)
		remark = "后台加款 " + remark
	} else {
		err = trader.Debit(req.UserID, amount.Abs(), "adjust", remark)
		remark = "后台扣款 " + remark
	}
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	_ = adminID
	utils.OKMsg(c, "调整成功", nil)
}

func AdminOrderList(c *gin.Context) {
	page, size := utils.Page(c)
	keyword := c.Query("keyword")
	status := c.Query("status")
	side := c.Query("side")
	q := database.DB.Model(&models.Order{})
	if keyword != "" {
		var uids []uint
		database.DB.Model(&models.User{}).Where("username LIKE ?", "%"+keyword+"%").
			Pluck("id", &uids)
		q = q.Where("user_id IN ?", uids)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if side != "" {
		q = q.Where("side = ?", side)
	}
	var total int64
	q.Count(&total)
	var list []models.Order
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AdminPositionList 持仓列表
// posRow 持仓行 DTO：在原始字段基础上补实时浮动盈亏（持仓中按现价计算）
type posRow struct {
	models.Position
	FloatProfit string `json:"floatProfit"` // 浮动盈亏（持仓中有值；已平仓为 0）
}

func AdminPositionList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Position{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.Position
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	// 持仓中：按现价补实时浮动盈亏（同交易端公式）；已平仓保留历史 profit
	out := make([]posRow, 0, len(list))
	for i := range list {
		pos := list[i]
		fp := "0.00"
		if pos.Status == 1 {
			price := market.GetPrice(pos.ProductID)
			if price.IsZero() {
				var pp models.Product
				if database.DB.First(&pp, pos.ProductID).Error == nil {
					price = pp.LastPrice
				}
			}
			fp = trader.UnrealizedProfit(&pos, price).Truncate(2).String()
		}
		out = append(out, posRow{Position: pos, FloatProfit: fp})
	}
	// 汇总：当前持仓中(买涨/买跌)总手数 + 浮动盈亏，供管理端看总体情况
	utils.OK(c, gin.H{
		"list": out, "total": total, "page": page, "size": size,
		"summary": positionSummary(),
	})
}

// positionSummary 持仓汇总：买涨/买跌总手数 + 各自浮动盈亏（现价口径，同交易端公式）。
func positionSummary() map[string]interface{} {
	type row struct {
		ProductID uint
		Side      int
		Vol       int64
		VolOpen   decimal.Decimal // SUM(volume*open_price)
	}
	var rows []row
	database.DB.Model(&models.Position{}).
		Select("product_id, side, COALESCE(SUM(volume),0) AS vol, COALESCE(SUM(volume*open_price),0) AS vol_open").
		Where("status = 1").Group("product_id, side").Find(&rows)
	var longLots, shortLots int64
	var longFloat, shortFloat decimal.Decimal
	for _, r := range rows {
		var p models.Product
		database.DB.First(&p, r.ProductID)
		price := market.GetPrice(r.ProductID)
		if price.IsZero() {
			price = p.LastPrice
		}
		unit := p.UnitValue
		if unit.IsZero() {
			unit = decimal.NewFromInt(1)
		}
		lev := decimal.NewFromInt(int64(p.LeverageOr10()))
		vol := decimal.NewFromInt(r.Vol)
		// 浮动 = (现价×总手数 − 累计(开仓价×手数)) × unit / 杠杆；买跌反向
		diff := price.Mul(vol).Sub(r.VolOpen)
		if r.Side == 2 {
			diff = diff.Neg()
		}
		f := diff.Mul(unit).Div(lev)
		if r.Side == 1 {
			longLots += r.Vol
			longFloat = longFloat.Add(f)
		} else {
			shortLots += r.Vol
			shortFloat = shortFloat.Add(f)
		}
	}
	trunc := func(v decimal.Decimal) string { return v.Truncate(2).String() }
	return map[string]interface{}{
		"longLots":   longLots,
		"shortLots":  shortLots,
		"longFloat":  trunc(longFloat),
		"shortFloat": trunc(shortFloat),
		"netFloat":   trunc(longFloat.Add(shortFloat)),
	}
}

// DepositList 充值列表
func DepositList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Deposit{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.Deposit
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// DepositAudit 充值审核
func DepositAudit(c *gin.Context) {
	var req struct {
		ID     uint `json:"id" binding:"required"`
		Status int  `json:"status"` // 1通过 2拒绝
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var d models.Deposit
	if err := database.DB.First(&d, req.ID).Error; err != nil || d.Status != 0 {
		utils.BadRequest(c, "申请不存在或已处理")
		return
	}
	now := time.Now()
	res := database.DB.Model(&models.Deposit{}).Where("id = ?", d.ID).Where("status = 0").
		Updates(map[string]interface{}{"status": req.Status, "audit_by": middleware.GetAdminID(c), "audit_at": now})
	if res.Error != nil {
		utils.ServerError(c, "审核失败")
		return
	}
	if res.RowsAffected == 0 {
		// 已被他人抢先处理（status 已非 0），防并发重复入账
		utils.BadRequest(c, "申请不存在或已处理")
		return
	}
	if req.Status == 1 {
		// 到账
		if err := trader.Credit(d.UserID, d.Amount, "deposit", "充值到账"); err != nil {
			utils.ServerError(c, "入账失败")
			return
		}
		// 累加该收款地址的到账金额（充值关联了收款地址时；原子 SQL 增量防并发覆盖）
		if d.AddressID > 0 {
			database.DB.Model(&models.PaymentAddress{}).
				Where("id = ?", d.AddressID).
				UpdateColumn("received_amount", gorm.Expr("received_amount + ?", d.Amount))
		}
		notify.Createf(d.UserID, "deposit", "充值到账", "您的 %s 充值已到账。", d.Amount.String())
	} else if req.Status == 2 {
		notify.Createf(d.UserID, "deposit", "充值被拒绝", "您的 %s 充值申请未通过。", d.Amount.String())
	}
	// 充值审核改变了待审核数量/平台资产，失效看板与流水缓存
	cache.InvalidateStats()
	cache.InvalidateWalletLog(d.UserID)
	utils.OKMsg(c, "审核成功", nil)
}

// WithdrawList 提现列表
func WithdrawList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Withdraw{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.Withdraw
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// WithdrawAudit 提现审核
func WithdrawAudit(c *gin.Context) {
	var req struct {
		ID     uint   `json:"id" binding:"required"`
		Status int    `json:"status"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var w models.Withdraw
	if err := database.DB.First(&w, req.ID).Error; err != nil || w.Status != 0 {
		utils.BadRequest(c, "申请不存在或已处理")
		return
	}
	now := time.Now()
	res := database.DB.Model(&models.Withdraw{}).Where("id = ?", w.ID).Where("status = 0").
		Updates(map[string]interface{}{"status": req.Status, "remark": req.Remark, "audit_by": middleware.GetAdminID(c), "audit_at": now})
	if res.Error != nil {
		utils.ServerError(c, "审核失败")
		return
	}
	if res.RowsAffected == 0 {
		// 已被他人抢先处理（status 已非 0），防并发重复放款/退款
		utils.BadRequest(c, "申请不存在或已处理")
		return
	}
	if req.Status == 1 {
		// 出金：解冻冻结金额（原子增量）
		trader.AdjustWallet(w.UserID, decimal.Zero, w.Amount.Neg(), "withdraw", w.Amount.Neg(), "提现成功")
		notify.Createf(w.UserID, "withdraw", "提现成功", "您的 %s 提现已通过并打款。", w.Amount.String())
	} else if req.Status == 2 {
		// 拒绝：返还冻结金额到余额（原子增量）
		trader.AdjustWallet(w.UserID, w.Amount, w.Amount.Neg(), "withdraw_refund", w.Amount, "提现拒绝返还")
		notify.Createf(w.UserID, "withdraw", "提现被拒绝", "您的 %s 提现申请未通过，金额已退回余额。", w.Amount.String())
	}
	// 提现审核改变了待审核数量/平台资产，失效看板与流水缓存
	cache.InvalidateStats()
	cache.InvalidateWalletLog(w.UserID)
	utils.OKMsg(c, "审核成功", nil)
}

// CreateControlRule 创建控盘规则（整段趋势引导）
// 入参 direction + floatRange(点数)：在 [startMinute, endMinute] 窗口内，每根按产品 float_step
// 固定涨/跌一个浮动量，窗口结束累计净变幅 = ±floatRange（涨+ 跌-）。
// 强校验：结束>开始、窗口分钟数 ≥ ceil(|净点数|/floatStep)（保证K线够用）。
func CreateControlRule(c *gin.Context) {
	var req struct {
		ProductID  uint   `json:"productId" binding:"required"`
		Direction  int    `json:"direction" binding:"required"` // 1涨 2跌
		FloatRange string `json:"floatRange"`
		StartMin   int64  `json:"startMinute"` // Unix
		EndMin     int64  `json:"endMinute"`
		NeedleHigh string `json:"needleHigh"`
		NeedleLow  string `json:"needleLow"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Direction != 1 && req.Direction != 2 {
		utils.BadRequest(c, "方向必须为 1(涨) 或 2(跌)")
		return
	}
	var p models.Product
	if err := database.DB.First(&p, req.ProductID).Error; err != nil {
		utils.BadRequest(c, "品种不存在")
		return
	}
	// 控盘依赖产品每根浮动数量(float_step)：未配置则无法保证逐根固定浮动，直接拒绝
	if !p.FloatStep.GreaterThan(decimal.Zero) {
		utils.BadRequest(c, "该品种需先配置每根浮动数量(floatStep)才能设置控盘")
		return
	}
	// 净点数：界面语义为正数，方向决定正负（涨+ 跌-）
	points, _ := decimal.NewFromString(req.FloatRange)
	if points.LessThan(decimal.Zero) {
		points = points.Neg()
	}
	var netPoints decimal.Decimal
	if req.Direction == 1 {
		netPoints = points
	} else {
		netPoints = points.Neg()
	}
	// 窗口时间
	from := req.StartMin
	to := req.EndMin
	if from <= 0 {
		from = time.Now().Unix()
	}
	if to <= 0 {
		to = from + 300
	}
	if to <= from {
		utils.BadRequest(c, "结束时间必须晚于开始时间")
		return
	}
	// 窗口宽度必须足够走完净点数：W(分钟) ≥ ceil(|net|/floatStep)，涨跌同规则
	if !netPoints.IsZero() {
		w := (to - from) / 60
		need := netPoints.Abs().Div(p.FloatStep).Ceil()
		if int64(need.IntPart()) > w {
			utils.BadRequest(c, fmt.Sprintf("窗口至少需 %s 分钟才能走完净目标（%s 点 / 每根 %s 点）",
				need.String(), netPoints.Abs().String(), p.FloatStep.String()))
			return
		}
	}
	// 目标价（展示用）= 窗口起点价 ± 净点数（控盘规则可超出当日涨跌边界，不在此限制）
	target := p.LastPrice.Add(netPoints)
	var nh, nl decimal.Decimal
	if req.NeedleHigh != "" {
		nh, _ = decimal.NewFromString(req.NeedleHigh)
	}
	if req.NeedleLow != "" {
		nl, _ = decimal.NewFromString(req.NeedleLow)
	}
	// 同一品种：旧的有效规则立即置为已结束，避免重叠打架
	database.DB.Model(&models.ControlRule{}).
		Where("product_id = ? AND status IN (0,1)", req.ProductID).Update("status", 2)
	if err := database.DB.Create(&models.ControlRule{
		ProductID:   req.ProductID,
		Direction:   req.Direction,
		NetPoints:   netPoints,
		TargetPrice: target,
		FloatRange:  points,
		StartMinute: time.Unix(from, 0),
		EndMinute:   time.Unix(to, 0),
		NeedleHigh:  nh,
		NeedleLow:   nl,
		Status:      0,
		CreatedBy:   middleware.GetAdminID(c),
	}).Error; err != nil {
		utils.ServerError(c, "创建失败")
		return
	}
	// 失效该品种全部分钟级规则缓存（前缀删除，真实 key 含分钟），让引擎下一根即生效
	cache.DelPrefix(fmt.Sprintf("cache:ctrlrule:%d:", req.ProductID))
	utils.OKMsg(c, "控盘规则已创建", nil)
}

// ControlRuleList 控盘规则列表
func ControlRuleList(c *gin.Context) {
	var list []models.ControlRule
	database.DB.Order("id desc").Limit(100).Find(&list)
	utils.OK(c, list)
}

// CloseAllPositions 管理端强制平仓（日终强平按钮）：平掉全部持仓并撤掉全部挂单
func CloseAllPositions(c *gin.Context) {
	n, err := trader.ForceCloseAll()
	if err != nil {
		utils.ServerError(c, err.Error())
		return
	}
	pending := trader.CancelAllPending()
	utils.OKMsg(c, "已强制平仓 "+toString(n)+" 笔，撤单 "+toString(pending)+" 笔", nil)
}

// AdminForceCloseUser 平台强制干预：强平指定客户全部持仓
func AdminForceCloseUser(c *gin.Context) {
	var req struct {
		UserID uint `json:"userId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	trader.CloseUserPositions(req.UserID)
	utils.OKMsg(c, "已强平该客户全部持仓", nil)
}

func toString(n int) string {
	return decimal.NewFromInt(int64(n)).String()
}

// AdminMarketStats 上帝视角：平台总利润与全局头寸风险（7 次聚合走 15s 短缓存）
func AdminMarketStats(c *gin.Context) {
	stats, err := cache.StatMarket(func() (*cache.MarketStats, error) {
		var fee, share, userProfit, positionVolume decimal.Decimal
		database.DB.Model(&models.Order{}).
			Where("is_close = 0 AND status = 2").
			Select("COALESCE(SUM(fee),0)").Row().Scan(&fee)
		database.DB.Model(&models.ProfitShare{}).Where("type IN ('fee','loss')").
			Select("COALESCE(SUM(amount),0)").Row().Scan(&share)
		// 客户平仓总盈亏（正=平台付给客户）
		database.DB.Model(&models.Position{}).Where("status = 2").
			Select("COALESCE(SUM(profit),0)").Row().Scan(&userProfit)
		database.DB.Model(&models.Position{}).Where("status = 1").
			Select("COALESCE(SUM(volume),0)").Row().Scan(&positionVolume)
		var buyVol, sellVol decimal.Decimal
		database.DB.Model(&models.Position{}).Where("status = 1 AND side = 1").
			Select("COALESCE(SUM(volume),0)").Row().Scan(&buyVol)
		database.DB.Model(&models.Position{}).Where("status = 1 AND side = 2").
			Select("COALESCE(SUM(volume),0)").Row().Scan(&sellVol)

		// B-Book 系统承接量（做市对赌）
		var bbookVol decimal.Decimal
		database.DB.Model(&models.Order{}).
			Where("is_close = 0 AND status = 2 AND match_type = 2").
			Select("COALESCE(SUM(volume),0)").Row().Scan(&bbookVol)

		platformProfit := fee.Sub(share).Sub(userProfit)

		return &cache.MarketStats{
			PlatformProfit: platformProfit.String(),
			TotalFee:       fee.String(),
			SharePaid:      share.String(),
			UserNetProfit:  userProfit.String(),
			PositionVolume: positionVolume.String(),
			NetPosition:    buyVol.Sub(sellVol).String(),
			BBookVolume:    bbookVol.String(),
		}, nil
	})
	if err != nil {
		utils.ServerError(c, "加载市场统计失败")
		return
	}
	utils.OK(c, gin.H{
		"platformProfit": stats.PlatformProfit,
		"totalFee":       stats.TotalFee,
		"sharePaid":      stats.SharePaid,
		"userNetProfit":  stats.UserNetProfit,
		"positionVolume": stats.PositionVolume,
		"netPosition":    stats.NetPosition,
		"bbookVolume":    stats.BBookVolume,
		"mode":           market.Mode(),
		"freeze":         market.Freezed(),
		"tradingOpen":    market.TradingOpen(),
	})
}

// AdminMarketFreeze 暂停/恢复行情
func AdminMarketFreeze(c *gin.Context) {
	var req struct {
		Freeze bool `json:"freeze"`
	}
	_ = c.ShouldBindJSON(&req)
	market.SetFreeze(req.Freeze)
	utils.OKMsg(c, "操作成功", nil)
}

// AdminMarketMode 切换行情模式：random / guided / freeze
func AdminMarketMode(c *gin.Context) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Mode == "" {
		utils.BadRequest(c, "参数错误")
		return
	}
	market.SetMode(req.Mode)
	utils.OKMsg(c, "模式已切换", nil)
}

// AdminMarketHours 设置交易时间（"HH:MM" 分钟精度，00:00-23:59；start=end=00:00 表示全天 24 小时）
func AdminMarketHours(c *gin.Context) {
	var req struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	start, ok1 := market.ParseHHMM(req.Start)
	end, ok2 := market.ParseHHMM(req.End)
	if !ok1 || !ok2 {
		utils.BadRequest(c, "时间格式应为 HH:MM")
		return
	}
	market.SetTradingHours(start, end)
	// 持久化到 setting 表，重启后自动恢复
	market.PersistTradingHours()
	utils.OKMsg(c, "交易时间已设置", nil)
}

// AdminTradeSettings 读取交易设置（系统强平线等），管理端配置页展示
func AdminTradeSettings(c *gin.Context) {
	sM, eM := market.TradingSession()
	utils.OK(c, gin.H{
		"strongClose": market.StrongClose().String(),
		"startTime":   market.MinutesToStr(sM),
		"endTime":     market.MinutesToStr(eM),
		"trading24h":  sM == 0 && eM == 0,
		"strategy":    market.Strategy(),
	})
}

// AdminControlStrategy 设置系统控盘策略：none(关闭) / follow_big(跟随大手数) / reverse_big(反向大手数)
func AdminControlStrategy(c *gin.Context) {
	var req struct {
		Strategy string `json:"strategy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	switch req.Strategy {
	case "none", "follow_big", "reverse_big":
		market.SetStrategy(req.Strategy)
		utils.OKMsg(c, "系统控盘策略已更新", nil)
	default:
		utils.BadRequest(c, "策略参数无效")
	}
}

// AdminStrongClose 设置系统强平线：浮亏达权益（余额+冻结）的 ratio 比例时自动强平全部持仓。
// ratio 为 0~1（如 0.2 = 亏损达权益 20% 时强平）；0 表示关闭该强平线。
func AdminStrongClose(c *gin.Context) {
	var req struct {
		Ratio float64 `json:"ratio"` // 如 0.2
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Ratio < 0 || req.Ratio > 1 {
		utils.BadRequest(c, "强平线取值范围应为 0~1")
		return
	}
	market.SetStrongClose(decimal.NewFromFloat(req.Ratio))
	market.PersistStrongClose()
	utils.OKMsg(c, "系统强平线已设置", nil)
}

// AdminInjectOrder 控盘注入虚拟大单（盘口假象）
func AdminInjectOrder(c *gin.Context) {
	var req struct {
		Side  int    `json:"side"` // 1买 2卖
		Price string `json:"price"`
		Qty   string `json:"qty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	price, _ := decimal.NewFromString(req.Price)
	qty, _ := decimal.NewFromString(req.Qty)
	if price.IsZero() || qty.Sign() <= 0 {
		utils.BadRequest(c, "价格/数量不合法")
		return
	}
	market.InjectOrder(req.Side, price, qty)
	utils.OKMsg(c, "大单已注入", nil)
}

// AdminAgentTrans 管理端代理资金流水（穿透）
func AdminAgentTrans(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.ProfitShare{})
	if v := c.Query("type"); v != "" {
		q = q.Where("type = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.ProfitShare
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AdminWalletLogList 资金流水（管理端，全用户）
func AdminWalletLogList(c *gin.Context) {
	page, size := utils.Page(c)

	// 组装筛选条件（供列表与汇总复用）
	build := func() *gorm.DB {
		q := database.DB.Model(&models.WalletLog{})
		if v := c.Query("type"); v != "" {
			q = q.Where("type = ?", v)
		}
		if v := c.Query("userId"); v != "" {
			q = q.Where("user_id = ?", v)
		}
		if kw := c.Query("keyword"); kw != "" {
			var uids []uint
			like := "%" + kw + "%"
			database.DB.Model(&models.User{}).
				Where("username LIKE ? OR phone LIKE ?", like, like).Pluck("id", &uids)
			q = q.Where("user_id IN ?", uids)
		}
		return q
	}

	var total int64
	build().Count(&total)

	var list []models.WalletLog
	build().Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)

	// 附上用户名，便于前端直接展示
	type VO struct {
		models.WalletLog
		Username string `json:"username"`
	}
	uids := make([]uint, 0, len(list))
	for _, w := range list {
		uids = append(uids, w.UserID)
	}
	nameMap := map[uint]string{}
	if len(uids) > 0 {
		var users []models.User
		database.DB.Select("id, username").Where("id IN ?", uids).Find(&users)
		for _, u := range users {
			nameMap[u.ID] = u.Username
		}
	}
	out := make([]VO, 0, len(list))
	for _, w := range list {
		out = append(out, VO{WalletLog: w, Username: nameMap[w.UserID]})
	}

	// 汇总：总收入 / 总支出（按当前筛选条件）
	var sumIn, sumOut decimal.Decimal
	build().Where("amount > 0").Select("COALESCE(SUM(amount),0)").Row().Scan(&sumIn)
	build().Where("amount < 0").Select("COALESCE(SUM(amount),0)").Row().Scan(&sumOut)

	utils.OK(c, gin.H{
		"list":  out,
		"total": total,
		"page":  page,
		"size":  size,
		"summary": gin.H{
			"totalIn":  sumIn.String(),
			"totalOut": sumOut.String(),
			"net":      sumIn.Add(sumOut).String(),
		},
	})
}

// AdminAgentAdjust 代理额度批发管理（对应线下收款，正加负扣）
func AdminAgentAdjust(c *gin.Context) {
	var req struct {
		AgentID uint   `json:"agentId" binding:"required"`
		Amount  string `json:"amount" binding:"required"`
		Remark  string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.IsZero() {
		utils.BadRequest(c, "金额不合法")
		return
	}
	var agent models.Agent
	if err := database.DB.First(&agent, req.AgentID).Error; err != nil {
		utils.BadRequest(c, "代理商不存在")
		return
	}
	// 原子调整额度：balance += amount 且结果不为负（防与分润等并发丢失更新/负额度）
	res := database.DB.Exec(
		"UPDATE agents SET balance = balance + ? WHERE id = ? AND balance + ? >= 0",
		amount, req.AgentID, amount)
	if res.Error != nil {
		utils.ServerError(c, "额度调整失败")
		return
	}
	if res.RowsAffected == 0 {
		utils.BadRequest(c, "额度不足，无法扣除")
		return
	}
	database.DB.Create(&models.ProfitShare{
		AgentID: req.AgentID, Type: "quota", Amount: amount, Status: 1,
	})
	utils.OKMsg(c, "额度调整成功", nil)
}

// AdminClearInject 清空控盘注入
func AdminClearInject(c *gin.Context) {
	market.ClearInject()
	utils.OKMsg(c, "已清空注入", nil)
}

// ProductDelete 删除品种（存在订单数据时禁止删除）
func ProductDelete(c *gin.Context) {
	id := c.Param("id")
	var orders int64
	database.DB.Model(&models.Order{}).Where("product_id = ?", id).Count(&orders)
	if orders > 0 {
		utils.BadRequest(c, "该品种存在订单数据，无法删除")
		return
	}
	if err := database.DB.Delete(&models.Product{}, id).Error; err != nil {
		utils.ServerError(c, "删除失败")
		return
	}
	// 删除品种：失效全部品种缓存（单条/列表/按code 前缀）
	cache.InvalidateProducts(jsonID(c, "id"))
	if pid := jsonID(c, "id"); pid != 0 {
		trader.InvalidateProductParam(pid)
	}
	utils.OKMsg(c, "删除成功", nil)
}

// AdminUserDelete 删除C端用户（存在未平仓持仓时禁止删除）
func AdminUserDelete(c *gin.Context) {
	id := c.Param("id")
	var pos int64
	database.DB.Model(&models.Position{}).Where("user_id = ? AND status = 1", id).Count(&pos)
	if pos > 0 {
		utils.BadRequest(c, "该用户存在未平仓持仓，无法删除")
		return
	}
	// 级联删除用户及其关联数据
	database.DB.Where("user_id = ?", id).Delete(&models.Wallet{})
	database.DB.Where("user_id = ?", id).Delete(&models.WalletLog{})
	database.DB.Where("user_id = ?", id).Delete(&models.Order{})
	database.DB.Where("user_id = ?", id).Delete(&models.Position{})
	database.DB.Where("user_id = ?", id).Delete(&models.Deposit{})
	database.DB.Where("user_id = ?", id).Delete(&models.Withdraw{})
	if err := database.DB.Delete(&models.User{}, id).Error; err != nil {
		utils.ServerError(c, "删除失败")
		return
	}
	// 用户删除：失效用户档案缓存与统计看板
	uid := jsonID(c, "id")
	cache.InvalidateUser(uid)
	cache.InvalidateStats()
	utils.OKMsg(c, "删除成功", nil)
}

// AdminAgentDelete 删除代理商（存在下级代理或关联用户时禁止删除）
func AdminAgentDelete(c *gin.Context) {
	id := c.Param("id")
	var sub int64
	database.DB.Model(&models.Agent{}).Where("parent_id = ?", id).Count(&sub)
	if sub > 0 {
		utils.BadRequest(c, "该代理商存在下级代理，无法删除")
		return
	}
	var users int64
	database.DB.Model(&models.User{}).Where("agent_id = ?", id).Count(&users)
	if users > 0 {
		utils.BadRequest(c, "该代理商存在关联用户，无法删除")
		return
	}
	database.DB.Where("agent_id = ?", id).Delete(&models.AgentWithdraw{})
	database.DB.Where("agent_id = ?", id).Delete(&models.ProfitShare{})
	if err := database.DB.Delete(&models.Agent{}, id).Error; err != nil {
		utils.ServerError(c, "删除失败")
		return
	}
	// 代理商删除：失效代理商档案缓存与统计看板
	aid := jsonID(c, "id")
	cache.InvalidateAgent(aid)
	cache.InvalidateStats()
	utils.OKMsg(c, "删除成功", nil)
}

// AdminUserUpdate 编辑C端用户基本信息（手机号/邮箱/姓名/身份证/所属代理商/状态/实名）
func AdminUserUpdate(c *gin.Context) {
	var req struct {
		ID       uint   `json:"id" binding:"required"`
		Phone    string `json:"phone"`
		Email    string `json:"email"`
		RealName string `json:"realName"`
		IDCard   string `json:"idCard"`
		AgentID  uint   `json:"agentId"`
		Status   int    `json:"status"`
		Verified int    `json:"verified"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.ID == 0 {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Status != 0 && req.Status != 1 {
		utils.BadRequest(c, "状态值不合法")
		return
	}
	if req.Verified != 0 && req.Verified != 1 {
		utils.BadRequest(c, "实名状态不合法")
		return
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
		"phone": req.Phone, "email": req.Email, "real_name": req.RealName,
		"id_card": req.IDCard, "agent_id": req.AgentID,
		"status": req.Status, "verified": req.Verified,
	}).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	// 用户资料/状态变更：失效用户档案缓存（禁号即时生效）
	cache.InvalidateUser(req.ID)
	utils.OKMsg(c, "编辑成功", nil)
}

// AdminRealNameAudit 实名审核（总后台）
func AdminRealNameAudit(c *gin.Context) {
	var req struct {
		UserID uint   `json:"userId" binding:"required"`
		Status int    `json:"status"` // 2通过 3驳回
		Remark string `json:"remark"` // 驳回原因（驳回时必填）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Status != 2 && req.Status != 3 {
		utils.BadRequest(c, "状态值不合法")
		return
	}
	if req.Status == 3 && strings.TrimSpace(req.Remark) == "" {
		utils.BadRequest(c, "驳回时必须填写备注")
		return
	}
	var u models.User
	if err := database.DB.First(&u, req.UserID).Error; err != nil {
		utils.BadRequest(c, "用户不存在")
		return
	}
	updates := map[string]interface{}{"real_name_status": req.Status}
	if req.Status == 2 {
		updates["verified"] = 1
		updates["real_name_remark"] = ""
	} else {
		updates["real_name_remark"] = strings.TrimSpace(req.Remark)
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", u.ID).Updates(updates).Error; err != nil {
		utils.ServerError(c, "审核失败")
		return
	}
	if req.Status == 2 {
		notify.Createf(u.ID, "realname", "实名认证通过", "您的实名认证已通过审核。")
	} else {
		notify.Createf(u.ID, "realname", "实名认证被驳回", "您的实名认证未通过，原因：%s。请修改后重新提交。", req.Remark)
	}
	utils.OKMsg(c, "审核成功", nil)
}
