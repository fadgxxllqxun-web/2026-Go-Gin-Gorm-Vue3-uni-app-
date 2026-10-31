package controllers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"server/cache"
	"server/config"
	"server/database"
	"server/models"
	"server/services/market"
	"server/services/trader"
	"server/utils"
)

// emailRegex 简单邮箱格式校验
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// ==================== 前端web 兼容层：/trader/* (code:0 / x-token) ====================

func traderOK(c *gin.Context, data interface{}) {
	c.JSON(200, gin.H{"code": 0, "msg": "success", "data": data})
}

// traderUser 解析 x-token / Bearer，返回当前C端用户（用户档案走 Redis 缓存，
// 禁号/改资料 由写路径 cache.InvalidateUser 主动失效）
func traderUser(c *gin.Context) *models.User {
	raw := c.GetHeader("x-token")
	if raw == "" {
		raw = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	}
	if raw == "" {
		return nil
	}
	claims, err := utils.ParseToken(raw)
	if err != nil || claims.UserType != "user" {
		return nil
	}
	var u models.User
	// 查用户档案（L2 命中时反序列化进 u；miss 回源 DB 并回填）
	// user 不存在时 DB.First 返回 ErrRecordNotFound，u 保持零值
	if e := cache.UserByID(&u, claims.UserID, func() error {
		return database.DB.First(&u, claims.UserID).Error
	}); e == nil && u.ID != 0 && u.Status == 1 {
		return &u
	}
	return nil
}

// --- 产品模型映射 ---
func productDTO(p *models.Product) gin.H {
	price := p.LastPrice
	if price.IsZero() {
		price = p.OpenPrice
	}
	prev := market.DayBase(*p) // 昨日收盘（涨跌幅/当日边界基准）
	change := price.Sub(prev)
	changePct := decimal.Zero
	if !prev.IsZero() {
		changePct = change.Div(prev).Mul(decimal.NewFromInt(100))
	}
	decimals := 2
	if !p.TickSize.IsZero() {
		decimals = int(-p.TickSize.Exponent())
		if decimals < 0 {
			decimals = 0
		}
	}
	unit := p.UnitValue
	if unit.IsZero() {
		unit = decimal.NewFromInt(1)
	}
	dayHigh := market.DayHigh(p.ID, p)
	dayLow := market.DayLow(p.ID, p)
	dayOpenVal := market.DayOpen(p.ID)
	if dayOpenVal.IsZero() {
		dayOpenVal = prev // 兜底用昨收
	}
	return gin.H{
		"id": p.ID, "code": p.Code, "name": p.Name,
		"price": price, "change": change, "changePct": changePct,
		"open": dayOpenVal, "high": dayHigh, "low": dayLow, "prevClose": prev,
		"volume": market.Volume(p.ID), "amount": market.Amount(p.ID),
		"priceDecimals": decimals, "tradeOpen": p.Status == 1,
		"leverage": p.LeverageOr10(), "multiplier": unit,
		"marginRate": p.MarginRate,
		"minQty": 1, "maxQty": p.MaxVolume,
		"feeRate": p.FeeRate, "feeFixed": p.FeePerHand,
		"floatStep": p.FloatStep,
		"mode":      "FUTURES_MODE",
	}
}

// getProductByCode 按 code 取品种（走 Redis 单条缓存，Ticker/Klines/Depth 每请求经此）
func productByCode(code string) *models.Product {
	var p models.Product
	if e := cache.GetOrSetObject(cache.ProductCodeKey(code), &p, cache.TTLProductCode, func() error {
		return database.DB.Where("code = ?", code).First(&p).Error
	}); e != nil || p.ID == 0 {
		return nil
	}
	return &p
}

// TraderRegister 注册：{email, password, inviteCode, nickname}
// 用邮箱号注册；邀请码必填：必须匹配到有效代理商才能注册成功（用户归属该代理）。
func TraderRegister(c *gin.Context) {
	var req struct {
		Email      string `json:"email"`
		Code       string `json:"code"`
		Password   string `json:"password"`
		InviteCode string `json:"inviteCode"`
		Nickname   string `json:"nickname"`
	}
	_ = c.ShouldBindJSON(&req)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "邮箱或密码不能为空", "data": nil})
		return
	}
	if !emailRegex.MatchString(req.Email) {
		c.JSON(200, gin.H{"code": 1, "msg": "邮箱格式不正确", "data": nil})
		return
	}
	// 邮箱验证码校验（仅当后台开启「邮箱验证」时校验；关闭时注册直接放行，不校验验证码）
	if needMailCode() && !verifyEmailCode(req.Email, "register", req.Code) {
		c.JSON(200, gin.H{"code": 1, "msg": "验证码错误或已过期，请重新获取", "data": nil})
		return
	}
	// 邀请码必填 + 必须匹配到启用中的代理，否则注册失败
	if req.InviteCode == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "请填写代理商邀请码", "data": nil})
		return
	}
	var a models.Agent
	if err := database.DB.Where("invite_code = ? AND status = 1", req.InviteCode).First(&a).Error; err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "邀请码无效，请检查后重试", "data": nil})
		return
	}

	var count int64
	database.DB.Model(&models.User{}).Where("username = ? OR email = ?", req.Email, req.Email).Count(&count)
	if count > 0 {
		c.JSON(200, gin.H{"code": 1, "msg": "邮箱已注册", "data": nil})
		return
	}
	hash, _ := database.Hash(req.Password)
	user := models.User{Username: req.Email, Email: req.Email, Password: hash, Status: 1, AgentID: a.ID}
	// 系统关闭实名门槛时：注册即默认视为已实名（无需认证即可出入金/交易）
	if !needRealNameForce() {
		user.RealNameStatus = 2
		user.Verified = 1
	}
	if req.Nickname != "" {
		user.Nickname = req.Nickname
	}
	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "注册失败", "data": nil})
		return
	}
	database.DB.Create(&models.Wallet{UserID: user.ID, Balance: decimal.Zero, Frozen: decimal.Zero})
	// 用户注册：失效统计看板（用户数变更）+ 用户档案缓存（避免 traderUser 命中「不存在」脏值返回 401）
	cache.InvalidateStats()
	cache.InvalidateUser(user.ID)
	token, _ := utils.SignUserToken(user.ID, user.Username)
	traderOK(c, gin.H{"token": token, "user": userDTO(&user)})
}

// TraderLogin 登录：{account, password}
func TraderLogin(c *gin.Context) {
	var req struct {
		Account  string `json:"account"`
		Username string `json:"username"` // 兼容前端按用户名登录
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误", "data": nil})
		return
	}
	account := req.Account
	if account == "" {
		account = req.Username
	}
	if account == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误", "data": nil})
		return
	}
	// 优先按用户名/手机号精确匹配；避免 phone 模糊匹配导致跨用户误登
	var user models.User
	if err := database.DB.Where("username = ? OR phone = ?", account, account).Order("id asc").First(&user).Error; err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "账号不存在", "data": nil})
		return
	}
	if user.Status != 1 {
		c.JSON(200, gin.H{"code": 1, "msg": "账号已被禁用", "data": nil})
		return
	}
	if !database.CheckPwd(user.Password, req.Password) {
		c.JSON(200, gin.H{"code": 1, "msg": "密码错误", "data": nil})
		return
	}
	now := time.Now()
	database.DB.Model(&user).Update("last_login", now)
	token, _ := utils.SignUserToken(user.ID, user.Username)
	traderOK(c, gin.H{"token": token, "user": userDTO(&user)})
}

func userDTO(u *models.User) gin.H {
	hasFundPwd := u.FundPwd != ""
	// 兼容旧数据：Verified=1 映射为 已通过
	realStatus := u.RealNameStatus
	if realStatus == 0 && u.Verified == 1 {
		realStatus = 2
	}
	nickname := u.Nickname
	if nickname == "" {
		nickname = u.Username
	}
	return gin.H{
		"id": u.ID, "username": u.Username, "nickname": nickname,
		"avatar": u.Avatar,
		"phone":  u.Phone, "realName": u.RealName, "idCard": u.IDCard,
		"realNameStatus": realStatus,
		"realNameRemark": u.RealNameRemark,
		"hasFundPassword": hasFundPwd, "agentId": u.AgentID,
		"createdAt": u.CreatedAt.Format("2006-01-02"),
	}
}

// TraderProducts 行情-产品列表（走 Redis 列表缓存，避免每请求全表查）
func TraderProducts(c *gin.Context) {
	list, err := cache.ActiveProducts(func() ([]models.Product, error) {
		var list []models.Product
		if err := database.DB.Where("status = 1").Order("sort asc").Find(&list).Error; err != nil {
			return nil, err
		}
		return list, nil
	})
	if err != nil {
		var l []models.Product
		database.DB.Where("status = 1").Order("sort asc").Find(&l)
		list = l
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, productDTO(&list[i]))
	}
	traderOK(c, out)
}

// TraderOrderEstimate 下单资金预估：后端按下单同款公式计算保证金/开仓手续费/冻结总额，
// 并返回该用户当前最多可买手数。
// 价格口径：市价单用当前行情价，限价单用委托价——与 OpenOrder 下单逻辑一致。
// TraderOrderEstimate 下单资金预估：后端按下单同款公式计算保证金/开仓手续费/冻结总额/最多可买手数。
// 入参：code(产品代码)、price(当前价格，前端传)、qty(手数)。后端统一用前端传入的价格计算，
// 保证前端展示与后端实际冻结完全一致。
func TraderOrderEstimate(c *gin.Context) {
	var productID uint
	code := c.Query("code")
	if v := c.Query("productId"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			productID = uint(n)
		}
	}
	volume, _ := strconv.Atoi(c.Query("qty"))
	pf := func(code string) gin.H {
		return gin.H{"code": code, "margin": "0", "openFee": "0", "total": "0", "price": "0", "maxBuyable": 0}
	}
	if volume <= 0 {
		traderOK(c, pf(code))
		return
	}
	// 当前价格（前端传入，下单也用它，保证一致）
	var p models.Product
	if code != "" {
		database.DB.Where("code = ?", code).First(&p)
	} else {
		database.DB.First(&p, productID)
	}
	if p.ID == 0 {
		traderOK(c, pf(code))
		return
	}
	productID = p.ID
	// 当前价格：优先用前端传入的 price，未传则回退行情价/最新价（兼容旧调用）
	price, _ := decimal.NewFromString(c.Query("price"))
	if price.IsZero() {
		price = market.GetPrice(productID)
	}
	if price.IsZero() {
		price = p.LastPrice
	}
	margin := trader.CalcMargin(&p, price, volume)
	openFee := trader.CalcFee(&p, price, volume)

	// ==== 最多可买手数：受 单笔MaxVolume、MaxPosition 持仓上限、以及「可用余额/每手保证金」共同约束 ====
	var availQty int
	availQty = p.MaxVolume
	if availQty <= 0 {
		availQty = 100
	}
	if p.MaxPosition > 0 {
		var curVolume int
		database.DB.Model(&models.Position{}).
			Where("product_id = ? AND status = 1", productID).
			Select("COALESCE(SUM(volume),0)").Row().Scan(&curVolume)
		if cq := p.MaxPosition - curVolume; cq > 0 && cq < availQty {
			availQty = cq
		}
	}
	if availQty < 0 {
		availQty = 0
	}
	// 按余额可买：可用余额 ÷ 每手保证金（取整），保证金用与下单一致的 CalcMargin（固定/比例均正确）
	if u := traderUser(c); u != nil {
		if w := trader.GetWallet(u.ID); w != nil && w.Balance.GreaterThan(decimal.Zero) {
			marginOne := trader.CalcMargin(&p, price, 1)
			if marginOne.GreaterThan(decimal.Zero) {
				byBalance := int(w.Balance.Div(marginOne).IntPart())
				if byBalance > 0 && byBalance < availQty {
					availQty = byBalance
				}
			}
		}
	}
	traderOK(c, gin.H{
		"code":       p.Code,
		"margin":     margin.String(),
		"openFee":    openFee.String(),
		"total":      margin.String(), // 下单只冻结保证金，无手续费预付
		"price":      price.String(),
		"marginOne":  trader.CalcMargin(&p, price, 1).String(), // 每手保证金（前端可展示/换算）
		"maxBuyable": availQty, // 可开仓手数：产品上限 × 持仓上限 × 余额/每手保证金
	})
}


// TraderTicker 单个产品行情
func TraderTicker(c *gin.Context) {
	code := c.Query("code")
	p := productByCode(code)
	if p == nil {
		traderOK(c, gin.H{})
		return
	}
	traderOK(c, productDTO(p))
}

// TraderKlines 多周期K线：{code, period, limit} → [{time,open,high,low,close,volume}]
func TraderKlines(c *gin.Context) {
	code := c.Query("code")
	p := productByCode(code)
	if p == nil {
		traderOK(c, []gin.H{})
		return
	}
	limit := 300
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	period := 1
	switch c.Query("period") {
	case "5m":
		period = 5
	case "15m":
		period = 15
	case "30m":
		period = 30
	case "1h":
		period = 60
	case "1d":
		period = 60 * 24
	}
	var raw []models.Kline1min
	database.DB.Where("product_id = ?", p.ID).Order("id desc").Limit(period * limit).Find(&raw)
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}
	out := make([]gin.H, 0)
	segSize := (period / 60) * 60
	if period < 60 {
		segSize = 0
	}
	count := (len(raw) / period) * period
	for i := 0; i < count; i += period {
		blk := raw[i : i+period]
		o := blk[0].Open
		cl := blk[len(blk)-1].Close
		h, l := o, o
		var vol, amt decimal.Decimal
		for _, k := range blk {
			if k.High.GreaterThan(h) {
				h = k.High
			}
			if k.Low.LessThan(l) {
				l = k.Low
			}
			vol = vol.Add(k.Volume)
			amt = amt.Add(k.Amount)
		}
		out = append(out, gin.H{
			"time": blk[len(blk)-1].Timestamp.Unix(),
			"open": o, "high": h, "low": l, "close": cl,
			"volume": vol, "amount": amt,
		})
	}
	_ = segSize
	// 追加当前正在形成的1分钟K线（不落地库），使刷新/重连后与实时最后一根一致
	if period == 1 {
		if o, h, l, cl, mt, ok := market.CurrentBar(p.ID); ok && !mt.IsZero() {
			var lastT int64
			if n := len(out); n > 0 {
				lastT = out[n-1]["time"].(int64)
			}
			t := mt.Unix()
			if t > lastT {
				out = append(out, gin.H{
					"time": t, "open": o, "high": h, "low": l, "close": cl,
					"volume": market.CurrentBarVolume(p.ID), "amount": market.CurrentBarAmount(p.ID),
				})
			}
		}
	}
	traderOK(c, out)
}

// TraderDepth 盘口：{code} → {bids, asks}
func TraderDepth(c *gin.Context) {
	code := c.Query("code")
	p := productByCode(code)
	if p == nil {
		traderOK(c, gin.H{"bids": []gin.H{}, "asks": []gin.H{}})
		return
	}
	book := market.GetBook(p.ID, 10)
	bids := make([]gin.H, 0, len(book.Buys))
	for _, b := range book.Buys {
		bids = append(bids, gin.H{"price": b.Price, "qty": b.Quantity})
	}
	asks := make([]gin.H, 0, len(book.Sells))
	for _, s := range book.Sells {
		asks = append(asks, gin.H{"price": s.Price, "qty": s.Quantity})
	}
	traderOK(c, gin.H{"bids": bids, "asks": asks})
}

// TraderTime 市场时间
func TraderTime(c *gin.Context) {
	start, end := market.TradingSession()
	enable24h := start == 0 && end == 0
	traderOK(c, gin.H{
		"marketOpen": market.TradingOpen(), "enable24h": enable24h,
		"openTime":  market.MinutesToStr(start),
		"closeTime": market.MinutesToStr(end),
		"settleTime": "21:05",
		"serverTime": time.Now().Unix(),
	})
}

// --- 需登录 ---

// TraderUserInfo 用户信息
func TraderUserInfo(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	traderOK(c, userDTO(u))
}

// TraderAsset 资产概览：{totalAsset, available, totalMargin, floating, riskRate}
func TraderAsset(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	total, available, margin, floating := summaryAsset(u.ID)
	risk := decimal.Zero
	if !margin.IsZero() {
		risk = total.Add(floating).Div(margin)
	}
	traderOK(c, gin.H{
		"totalAsset": total, "available": available, "totalMargin": margin,
		"floating": floating, "riskRate": risk,
		"balance": available,
	})
}

func summaryAsset(uid uint) (total, available, margin, floating decimal.Decimal) {
	wal := trader.GetWallet(uid)
	total = wal.Balance.Add(wal.Frozen)
	available = wal.Balance
	var plist []models.Position
	database.DB.Where("user_id = ? AND status = 1", uid).Find(&plist)
	for _, pos := range plist {
		margin = margin.Add(pos.Margin)
		price := market.GetPrice(pos.ProductID)
		if !price.IsZero() {
			floating = floating.Add(trader.UnrealizedProfit(&pos, price))
		}
	}
	return
}

// TraderPositions 持仓
func TraderPositions(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var plist []models.Position
	database.DB.Where("user_id = ? AND status = 1", u.ID).Order("id desc").Find(&plist)
	// 批量取品种（消除 N+1），走 product 单条缓存
	prodIDs := make([]uint, 0, len(plist))
	prodSet := map[uint]bool{}
	for _, pos := range plist {
		if !prodSet[pos.ProductID] {
			prodIDs = append(prodIDs, pos.ProductID)
			prodSet[pos.ProductID] = true
		}
	}
	prodMap := batchProducts(prodIDs)
	out := make([]gin.H, 0)
	for _, pos := range plist {
		var p models.Product
		if pp, ok := prodMap[pos.ProductID]; ok {
			p = pp
		}
		price := market.GetPrice(pos.ProductID)
		if price.IsZero() {
			price = p.LastPrice
		}
		pnl := trader.UnrealizedProfit(&pos, price)
		side := "buy"
		if pos.Side == 2 {
			side = "sell"
		}
		out = append(out, gin.H{
			"id": pos.ID, "name": p.Name, "code": p.Code, "side": side,
			"qty": pos.Volume, "openPrice": pos.OpenPrice, "nowPrice": price,
			"floatingPnl": pnl, "margin": pos.Margin,
			"stopLoss": pos.StopLoss, "stopProfit": pos.TakeProfit,
		})
	}
	traderOK(c, out)
}

// batchProducts 按 ID 批量取品种（优先走单条 Redis 缓存，miss 批量回源数据库），
// 用于消除持仓/订单列表里的 N+1 逐条查询。
func batchProducts(ids []uint) map[uint]models.Product {
	res := make(map[uint]models.Product, len(ids))
	miss := make([]uint, 0)
	for _, id := range ids {
		var p models.Product
		if e := cache.GetOrSetObject(cache.ProductKey(id), &p, cache.TTLProduct, func() error {
			return database.DB.First(&p, id).Error
		}); e == nil && p.ID > 0 {
			res[id] = p
		} else {
			miss = append(miss, id)
		}
	}
	if len(miss) > 0 {
		var list []models.Product
		database.DB.Where("id IN ?", miss).Find(&list)
		for _, p := range list {
			res[p.ID] = p
			_ = cache.SetObject(cache.ProductKey(p.ID), &p, cache.TTLProduct) // 回填，加速后续请求
		}
	}
	return res
}

// TraderPlaceOrder 下单
func TraderPlaceOrder(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	var req struct {
		ProductID     uint    `json:"productId"`
		Side          string  `json:"side"`
		OrderType     string  `json:"orderType"`
		Price         float64 `json:"price"`
		Qty           float64 `json:"qty"`
		StopProfit    float64 `json:"stopProfit"`
		StopLoss      float64 `json:"stopLoss"`
		ClientOrderID string  `json:"clientOrderId"` // 客户端幂等键（可选；防重复下单）
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ProductID == 0 || req.Qty <= 0 {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误", "data": nil})
		return
	}
	side := 1
	if req.Side == "sell" {
		side = 2
	}
	orderType := 1
	if req.OrderType == "limit" {
		orderType = 2
	}
	price := decimal.NewFromFloat(req.Price)
	sl := decimal.NewFromFloat(req.StopLoss)
	tp := decimal.NewFromFloat(req.StopProfit)
	var cid *string
	if req.ClientOrderID != "" {
		cid = &req.ClientOrderID
	}
	order, _, err := trader.OpenOrder(u, req.ProductID, side, orderType, int(req.Qty), price, sl, tp, cid)
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": err.Error(), "data": nil})
		return
	}
	status := "filled"
	if order.Status == models.OrderPending {
		status = "pending"
	}
	traderOK(c, gin.H{"Status": status, "Msg": "下单成功", "orderId": order.ID})
}

// TraderCancelOrder 撤单
func TraderCancelOrder(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	var req struct {
		OrderID uint `json:"orderId"`
	}
	_ = c.ShouldBindJSON(&req)
	if err := trader.CancelOrder(u.ID, req.OrderID); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": err.Error(), "data": nil})
		return
	}
	traderOK(c, gin.H{"msg": "撤单成功"})
}

// TraderClosePosition 平仓
func TraderClosePosition(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	var req struct {
		PositionID int `json:"positionId"`
	}
	_ = c.ShouldBindJSON(&req)
	pos, err := trader.ClosePosition(u.ID, req.PositionID, 0, false)
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": err.Error(), "data": nil})
		return
	}
	traderOK(c, gin.H{"pnl": pos.Profit})
}

// TraderCloseAll 一键平仓
func TraderCloseAll(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	// 逐个强平并返回累计已实现盈亏（realized pnl，与单笔平仓口径一致，B10）
	totalPnl := trader.CloseUserPositions(u.ID)
	traderOK(c, gin.H{"msg": "已全部平仓", "totalPnl": totalPnl})
}

// TraderSetTPSL 设置止盈止损（简化：更新持仓）
func TraderSetTPSL(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	var req struct {
		PositionID uint    `json:"positionId"`
		StopProfit float64 `json:"stopProfit"`
		StopLoss   float64 `json:"stopLoss"`
	}
	_ = c.ShouldBindJSON(&req)
	tp := decimal.NewFromFloat(req.StopProfit)
	sl := decimal.NewFromFloat(req.StopLoss)
	var pos models.Position
	if err := database.DB.Where("id = ? AND user_id = ?", req.PositionID, u.ID).First(&pos).Error; err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "持仓不存在", "data": nil})
		return
	}
	database.DB.Model(&models.Position{}).Where("id = ?", pos.ID).
		Updates(map[string]interface{}{"take_profit": tp, "stop_loss": sl})
	// 同步刷新风控内存快照，避免新 TP/SL 等到 30s 兜底才生效、或清空后仍按旧值误触发
	pos.TakeProfit = tp
	pos.StopLoss = sl
	if tp.IsZero() && sl.IsZero() {
		trader.UnregisterPositionForTriggers(&pos)
	} else {
		trader.RegisterPositionForTriggers(&pos)
	}
	traderOK(c, gin.H{"msg": "已设置"})
}

// 通用排序字段映射
func orderSide(s int) string {
	if s == 2 {
		return "sell"
	}
	return "buy"
}

// TraderCurrentOrders 当前委托（待成交）
func TraderCurrentOrders(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	page, size := traderPage(c)
	var list []models.Order
	database.DB.Where("user_id = ? AND status = ?", u.ID, models.OrderPending).Order("id desc").
		Offset((page - 1) * size).Limit(size).Find(&list)
	rows := make([]gin.H, 0, len(list))
	for _, o := range list {
		// status=0 待成交的挂单：仅当确有部分成交(0<Filled<Volume)才标 partial，
		// 否则是 pending(挂单中)——沿用旧逻辑把整单全标 partial 会让未成交挂单误显"部分成交"。
		oStatus := "pending"
		if o.Filled > 0 && o.Filled < o.Volume {
			oStatus = "partial"
		}
		row := gin.H{
			"id": o.ID, "productId": o.ProductID, "side": orderSide(o.Side),
			"orderType": terStr(o.OrderType, "market", "limit"),
			"price":     o.Price, "qty": o.Volume, "filledQty": o.Filled,
			"status": oStatus, "createdAt": o.CreatedAt,
		}
		// 部分成交：带出已成交部分的成交均价与实时浮盈（撤单弹窗展示用）
		if o.PositionID > 0 && o.Filled > 0 {
			var pos models.Position
			if err := database.DB.First(&pos, o.PositionID).Error; err == nil {
				var p models.Product
				if err := database.DB.First(&p, pos.ProductID).Error; err == nil {
					price := market.GetPrice(pos.ProductID)
					if price.IsZero() {
						price = p.LastPrice
					}
					row["avgPrice"] = pos.OpenPrice
					row["profit"] = trader.UnrealizedProfit(&pos, price)
				}
			}
		}
		rows = append(rows, row)
	}
	traderOK(c, gin.H{"list": rows, "total": len(rows), "page": page, "size": size})
}

// historyPosition 关联开仓委托对应的持仓，用于取平仓价/盈亏。
// 优先用订单的 PositionID；历史数据大量开仓单 PositionID=0（创建时未写入），
// 此时回退按 用户+品种+方向+开仓价 匹配持仓（取最新一条），保证平仓信息正常展示。
func historyPosition(o models.Order) *models.Position {
	var pos models.Position
	lookup := func() bool {
		if o.PositionID > 0 {
			return database.DB.First(&pos, o.PositionID).Error == nil
		}
		return false
	}
	if lookup() {
		return &pos
	}
	var best models.Position
	if err := database.DB.Where("user_id = ? AND product_id = ? AND side = ? AND open_price = ? AND status = 2",
		o.UserID, o.ProductID, o.Side, o.Price).
		Order("id desc").First(&best).Error; err == nil {
		return &best
	}
	return nil
}

// histStatus 订单状态 int → 历史列表字符串（pending/filled/canceled）
func histStatus(st models.OrderStatus) string {
	switch st {
	case models.OrderCanceled:
		return "canceled"
	case models.OrderHolding:
		return "filled"
	default:
		return "pending"
	}
}

// TraderHistoryOrders 历史订单
func TraderHistoryOrders(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	page, size := traderPage(c)
	// 只返回开仓委托（IsClose=0）；平仓是一条系统生成的关联记录，
	// 平仓价/盈亏统一从持仓中取，避免“委托价=成交均价=平仓价”的错误展示。
	var list []models.Order
	database.DB.Where("user_id = ? AND is_close = 0", u.ID).Order("id desc").
		Offset((page - 1) * size).Limit(size).Find(&list)
	rows := make([]gin.H, 0, len(list))
	for _, o := range list {
		// 成交均价取持仓开仓成交价（真实成交价），平仓价/盈亏取持仓平仓数据；
		// 状态返回旧字符串：pending待成交 / filled已成交 / canceled已撤单
		avgPrice := o.Price
		var closePrice *decimal.Decimal
		var pnl *decimal.Decimal
		if pos := historyPosition(o); pos != nil && pos.OpenPrice.Sign() > 0 {
			avgPrice = pos.OpenPrice
			if pos.Status == 2 && !pos.ClosePrice.IsZero() {
				cp := pos.ClosePrice
				closePrice = &cp
				prof := pos.Profit
				pnl = &prof
			}
		}
		row := gin.H{
			"id": o.ID, "productId": o.ProductID, "side": orderSide(o.Side),
			"orderType": terStr(o.OrderType, "market", "limit"),
			"price":     o.Price, "qty": o.Volume, "filledQty": o.Filled,
			"avgPrice": avgPrice, "closePrice": closePrice,
			"closeAmount": nil, "pnl": pnl, "fee": o.Fee,
			"status": histStatus(o.Status), "createdAt": o.CreatedAt,
		}
		if closePrice != nil {
			row["closeAmount"] = closePrice.Mul(decimal.NewFromInt(int64(o.Volume)))
		}
		rows = append(rows, row)
	}
	traderOK(c, gin.H{"list": rows, "total": len(rows), "page": page, "size": size})
}

// TraderMyTrades 我的成交
func TraderMyTrades(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	page, size := traderPage(c)
	var list []models.Order
	database.DB.Where("user_id = ? AND status = ?", u.ID, models.OrderHolding).Order("id desc").
		Offset((page - 1) * size).Limit(size).Find(&list)
	rows := make([]gin.H, 0, len(list))
	for _, o := range list {
		typ := "open"
		if o.IsClose == 1 {
			typ = "close"
		}
		rows = append(rows, gin.H{
			"id": o.ID, "productId": o.ProductID, "side": orderSide(o.Side),
			"type": typ, "price": o.Price, "qty": o.Volume,
			"amount": o.Price.Mul(decimal.NewFromInt(int64(o.Volume))),
			"fee":    o.Fee, "profit": o.Profit, "createdAt": o.CreatedAt,
		})
	}
	traderOK(c, gin.H{"list": rows, "total": len(rows), "page": page, "size": size})
}

// TraderFundLogs 资金流水
func TraderFundLogs(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	page, size := traderPage(c)
	var list []models.WalletLog
	database.DB.Where("user_id = ?", u.ID).Order("id desc").
		Offset((page - 1) * size).Limit(size).Find(&list)
	rows := make([]gin.H, 0, len(list))
	for _, x := range list {
		rows = append(rows, gin.H{
			"type": x.Type, "amount": x.Amount, "balance": x.Balance,
			"remark": x.Remark, "createdAt": x.CreatedAt,
		})
	}
	traderOK(c, gin.H{"list": rows, "total": len(rows), "page": page, "size": size})
}

// TraderWithdrawList 提现记录
func TraderWithdrawList(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var list []models.Withdraw
	database.DB.Where("user_id = ?", u.ID).Order("id desc").Find(&list)
	rows := make([]gin.H, 0, len(list))
	for _, w := range list {
		rows = append(rows, gin.H{
			"id": w.ID, "amount": w.Amount, "channel": w.Channel,
			"account": w.Account, "network": w.Network, "name": w.Name, "screenshot": w.Screenshot,
			"status": w.Status, "createdAt": w.CreatedAt,
		})
	}
	traderOK(c, gin.H{"list": rows, "total": len(rows)})
}

// TraderDepositList 充值记录
func TraderDepositList(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var list []models.Deposit
	database.DB.Where("user_id = ?", u.ID).Order("id desc").Find(&list)
	rows := make([]gin.H, 0, len(list))
	for _, d := range list {
		rows = append(rows, gin.H{
			"id": d.ID, "amount": d.Amount, "channel": d.Channel,
			"accountName": d.AccountName, "accountNo": d.AccountNo,
			"screenshot": d.Screenshot,
			"status":     d.Status, "remark": d.Remark, "createdAt": d.CreatedAt,
		})
	}
	traderOK(c, gin.H{"list": rows, "total": len(rows)})
}

// TraderApplyDeposit 充值申请（登记待审核，后台审核通过后自动入账）
func TraderApplyDeposit(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	var req struct {
		Amount     float64 `json:"amount"`
		Channel    string  `json:"channel"`
		AddressID  uint    `json:"addressId"` // 平台收款地址（充值展示选择的收款地址；无需付款人姓名/账号）
		AccountName string `json:"accountName"`
		AccountNo  string  `json:"accountNo"`
		Screenshot string  `json:"screenshot"`
		Remark     string  `json:"remark"`
	}
	_ = c.ShouldBindJSON(&req)
	amount := decimal.NewFromFloat(req.Amount)
	if amount.IsZero() || amount.Sign() < 0 {
		c.JSON(200, gin.H{"code": 1, "msg": "金额不合法", "data": nil})
		return
	}
	// 渠道合法且后台已开启
	if !channelDepositEnabled(req.Channel) {
		c.JSON(200, gin.H{"code": 1, "msg": "该充值渠道未开启", "data": nil})
		return
	}
	// 同一用户未处理的充值申请未到账前不允许重复提交
	var pending int64
	database.DB.Model(&models.Deposit{}).Where("user_id = ? AND status = 0", u.ID).Count(&pending)
	if pending > 0 {
		c.JSON(200, gin.H{"code": 1, "msg": "您已有一条待审核的充值申请", "data": nil})
		return
	}
	database.DB.Create(&models.Deposit{
		UserID: u.ID, Amount: amount, Channel: req.Channel, AddressID: req.AddressID,
		AccountName: req.AccountName, AccountNo: req.AccountNo,
		Screenshot: req.Screenshot, Remark: req.Remark, Status: 0,
	})
	traderOK(c, gin.H{"msg": "充值申请已提交，等待审核"})
}

// TraderApplyWithdraw 申请提现
func TraderApplyWithdraw(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if !requireRealName(c, u) {
		return
	}
	var req struct {
		Amount     float64 `json:"amount"`
		Channel    string  `json:"channel"`
		Account    string  `json:"account"`
		Network    string  `json:"network"` // USDT 时用户填写的所属网络；出金不再要求收款人姓名
		Screenshot string  `json:"screenshot"`
		Remark     string  `json:"remark"`
		FundPwd    string  `json:"fundPwd"`
	}
	_ = c.ShouldBindJSON(&req)
	amount := decimal.NewFromFloat(req.Amount)
	if amount.IsZero() || amount.Sign() < 0 {
		c.JSON(200, gin.H{"code": 1, "msg": "金额不合法", "data": nil})
		return
	}
	if !channelWithdrawEnabled(req.Channel) {
		c.JSON(200, gin.H{"code": 1, "msg": "该出金渠道未开启", "data": nil})
		return
	}
	if req.Account == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "请填写收款账号", "data": nil})
		return
	}
	// 资金密码必填且必须匹配（未设置资金密码不允许提现）
	if req.FundPwd == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "请填写资金密码", "data": nil})
		return
	}
	if u.FundPwd == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "请先设置资金密码", "data": nil})
		return
	}
	if !database.CheckPwd(u.FundPwd, req.FundPwd) {
		c.JSON(200, gin.H{"code": 1, "msg": "资金密码错误", "data": nil})
		return
	}
	// 余额校验+冻结（原子增量）
	if _, ok := trader.AdjustWallet(u.ID, amount.Neg(), amount, "withdraw", amount.Neg(), "提现申请冻结"); !ok {
		c.JSON(200, gin.H{"code": 1, "msg": "余额不足", "data": nil})
		return
	}
	if err := database.DB.Create(&models.Withdraw{
		UserID: u.ID, Amount: amount, Channel: req.Channel,
		Account: req.Account, Network: req.Network, Screenshot: req.Screenshot,
		Remark: req.Remark, Status: 0,
	}).Error; err != nil {
		// 建单失败回滚冻结：归还余额并释放 frozen，避免"余额扣了、冻结悬挂"的幽灵资金
		trader.AdjustWallet(u.ID, amount, amount.Neg(), "withdraw_rollback", amount, "提现申请失败回滚")
		c.JSON(200, gin.H{"code": 1, "msg": "提交失败，请稍后重试", "data": nil})
		return
	}
	traderOK(c, gin.H{"msg": "提现申请已提交"})
}

// TraderNotices 公告列表
func TraderNotices(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	// 只返回发给当前用户的公告（user_id = 本人）。不再把全局公告(user_id=0)广播给所有用户。
	var list []models.Notice
	database.DB.Where("user_id = ?", u.ID).Order("id desc").Limit(50).Find(&list)
	rows := make([]gin.H, 0, len(list))
	unread := 0
	for _, n := range list {
		if n.IsRead == 0 {
			unread++
		}
		rows = append(rows, gin.H{
			"id": n.ID, "title": n.Title, "content": n.Content, "type": n.Type,
			"read": n.IsRead == 1, "createdAt": n.CreatedAt,
		})
	}
	traderOK(c, gin.H{"list": rows, "unread": unread})
}

// TraderReadNotice 标记已读
func TraderReadNotice(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var req struct {
		NoticeID uint `json:"noticeId"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.NoticeID > 0 {
		database.DB.Model(&models.Notice{}).Where("id = ? AND user_id = ?", req.NoticeID, u.ID).Update("is_read", 1)
	} else {
		database.DB.Model(&models.Notice{}).Where("user_id = ?", u.ID).Update("is_read", 1)
	}
	traderOK(c, gin.H{"msg": "已读"})
}

// TraderAgentContact 所属代理联系方式
func TraderAgentContact(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	if u.AgentID == 0 {
		traderOK(c, nil)
		return
	}
	var a models.Agent
	if err := database.DB.First(&a, u.AgentID).Error; err != nil {
		traderOK(c, nil)
		return
	}
	traderOK(c, gin.H{"nickname": a.AgentName, "phone": a.Phone, "wechat": a.Wechat, "qq": a.QQ})
}

// TraderSendSms 模拟短信：返回 devCode 供前端弹出显示
func TraderSendSms(c *gin.Context) {
	var req struct {
		Phone string `json:"phone"`
		Scene string `json:"scene"`
	}
	_ = c.ShouldBindJSON(&req)
	traderOK(c, gin.H{"devCode": "123456", "code": "123456"})
}

// TraderFundPassword 设置资金密码
func TraderFundPassword(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var req struct {
		FundPassword string `json:"fundPassword"`
	}
	_ = c.ShouldBindJSON(&req)
	if len(req.FundPassword) < 6 {
		c.JSON(200, gin.H{"code": 1, "msg": "资金密码至少6位", "data": nil})
		return
	}
	hash, _ := database.Hash(req.FundPassword)
	database.DB.Model(&models.User{}).Where("id = ?", u.ID).Update("fund_pwd", hash)
	// 资金密码变更：失效用户档案缓存
	cache.InvalidateUser(u.ID)
	traderOK(c, gin.H{"msg": "设置成功"})
}

// TraderRealname 实名认证
func TraderRealname(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var req struct {
		RealName string `json:"realName"`
		IDCard   string `json:"idCard"`
		IDFront  string `json:"idFront"`
		IDBack   string `json:"idBack"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.RealName == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "请填写姓名", "data": nil})
		return
	}
	if req.IDCard == "" {
		c.JSON(200, gin.H{"code": 1, "msg": "请填写身份证号", "data": nil})
		return
	}
	// 图片字段允许留空（兼容尚未上传正反面的老提交），但姓名+身份证号必填
	// 已通过的实名不允许重新提交
	if u.RealNameStatus == 2 {
		c.JSON(200, gin.H{"code": 1, "msg": "您已完成实名认证", "data": nil})
		return
	}
	// 保存实名资料，置为待审核(1)；由总后台用户管理页审核
	if err := database.DB.Model(&models.User{}).Where("id = ?", u.ID).Updates(map[string]interface{}{
		"real_name": req.RealName, "id_card": req.IDCard,
		"id_front": req.IDFront, "id_back": req.IDBack,
		"real_name_remark": "",
		"real_name_status": 1,
	}).Error; err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "提交失败，请稍后重试", "data": nil})
		return
	}
	// 实名状态变更：失效用户档案缓存
	cache.InvalidateUser(u.ID)
	// 后台关注走 AdminLatestEvents 业务动态流；不再写全局公告（避免用户端看到他人实名申请）
	traderOK(c, gin.H{"msg": "已提交实名认证，等待审核"})
}

func traderPage(c *gin.Context) (int, int) {
	page, size := 1, 10
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			page = n
		}
	}
	if v := c.Query("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			size = n
		}
	}
	return page, size
}

func terStr(v int, if1, if2 string) string {
	if v == 2 {
		return if2
	}
	return if1
}

// TraderRecentTrades 最近成交明细
func TraderRecentTrades(c *gin.Context) {
	code := c.Query("code")
	p := productByCode(code)
	if p == nil {
		traderOK(c, []gin.H{})
		return
	}
	trades := market.RecentTrades(p.ID, 50)
	rows := make([]gin.H, 0, len(trades))
	for _, t := range trades {
		rows = append(rows, gin.H{
			"time": t.Time, "price": t.Price, "qty": t.Qty,
			"side": t.Side, "source": t.Source,
		})
	}
	traderOK(c, rows)
}

// TraderChangePassword 修改登录密码
func TraderChangePassword(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	_ = c.ShouldBindJSON(&req)
	if !database.CheckPwd(u.Password, req.OldPassword) {
		c.JSON(200, gin.H{"code": 1, "msg": "原密码错误", "data": nil})
		return
	}
	hash, _ := database.Hash(req.NewPassword)
	database.DB.Model(&models.User{}).Where("id = ?", u.ID).Update("password", hash)
	// 登录密码变更：失效用户档案缓存
	cache.InvalidateUser(u.ID)
	traderOK(c, gin.H{"msg": "修改成功"})
}

// TraderUpdateProfile 修改资料（昵称/头像）
func TraderUpdateProfile(c *gin.Context) {
	u := traderUser(c)
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	var req struct {
		Nickname string `json:"nickname"`
		Avatar   string `json:"avatar"`
	}
	_ = c.ShouldBindJSON(&req)
	updates := map[string]interface{}{}
	if req.Nickname != "" {
		updates["nickname"] = req.Nickname
	}
	if req.Avatar != "" {
		updates["avatar"] = req.Avatar
	}
	if len(updates) > 0 {
		database.DB.Model(&models.User{}).Where("id = ?", u.ID).Updates(updates)
		// 用户资料变更：失效用户档案缓存
		cache.InvalidateUser(u.ID)
	}
	traderOK(c, gin.H{"msg": "保存成功"})
}

// TraderUpload 交易客户端图片上传（trader token 鉴权，返回 traderOK code:0 格式）
func TraderUpload(c *gin.Context) {
	// L2：上传需登录，别人不得匿名向 /static 填文件
	if u := traderUser(c); u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "未选择文件", "data": nil})
		return
	}
	if file.Size > 10<<20 { // 10MB 上限
		c.JSON(200, gin.H{"code": 1, "msg": "文件过大，最大 10MB", "data": nil})
		return
	}
	dir := config.Cfg.Upload.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "目录创建失败", "data": nil})
		return
	}
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext == "" {
		ext = ".png"
	}
	allowed := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}
	if !allowed[ext] {
		c.JSON(200, gin.H{"code": 1, "msg": "仅支持图片文件", "data": nil})
		return
	}
	name := fmt.Sprintf("%d_%s%s", time.Now().UnixNano(), randStr(6), ext)
	dst := filepath.Join(dir, name)
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "保存失败", "data": nil})
		return
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s/static/%s", scheme, c.Request.Host, name)
	traderOK(c, gin.H{"url": url})
}
