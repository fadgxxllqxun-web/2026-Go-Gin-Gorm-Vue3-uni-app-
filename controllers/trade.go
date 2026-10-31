package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"server/cache"
	"server/database"
	"server/models"
	"server/services/market"
	"server/services/trader"
	"server/utils"
)

// ProductList C端行情-品种列表（走 Redis 列表缓存，避免每请求全表查）
func ProductList(c *gin.Context) {
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
	utils.OK(c, list)
}

// KlineList 获取K线（period: 1/5/15/30/60 分钟，由1分钟K线聚合）
// 1 分钟 K 线每品种每分钟才新增一根，原始序列走 30s 短缓存（key 含 period+limit 以区分聚合粒度）
func KlineList(c *gin.Context) {
	pid := c.Query("productId")
	limit := 300
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	period := 1
	if v := c.Query("period"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			period = n
		}
	}
	need := period * limit
	type KOut struct {
		Open      string `json:"open"`
		High      string `json:"high"`
		Low       string `json:"low"`
		Close     string `json:"close"`
		Volume    string `json:"volume"`
		Amount    string `json:"amount"`
		Timestamp string `json:"timestamp"`
	}
	// 拉取最近 need 根 1 分钟K线（走 30s 短缓存，miss 回源数据库并回填）
	key := fmt.Sprintf("cache:kline:%s:%d:%d", pid, period, limit)
	raw, err := cache.GetOrSet[[]models.Kline1min](key, 30*time.Second, func() ([]models.Kline1min, error) {
		var raw []models.Kline1min
		if e := database.DB.Where("product_id = ?", pid).Order("id desc").Limit(need).Find(&raw).Error; e != nil {
			return nil, e
		}
		return raw, nil
	})
	if err != nil {
		var l []models.Kline1min
		database.DB.Where("product_id = ?", pid).Order("id desc").Limit(need).Find(&l)
		raw = l
	}
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}
	// 取完整周期块（丢弃尾部不完整残留）
	count := (len(raw) / period) * period
	out := make([]KOut, 0, count/period)
	for i := 0; i < count; i += period {
		blk := raw[i : i+period]
		var vol, amt decimal.Decimal
		for _, k := range blk {
			vol = vol.Add(k.Volume)
			amt = amt.Add(k.Amount)
		}
		out = append(out, KOut{
			Open:      blk[0].Open.String(),
			High:      blksHigh(blk).String(),
			Low:       blksLow(blk).String(),
			Close:     blk[len(blk)-1].Close.String(),
			Volume:    vol.String(),
			Amount:    amt.String(),
			Timestamp: blk[len(blk)-1].Timestamp.Format("2006-01-02 15:04"),
		})
	}
	utils.OK(c, out)
}

func blksHigh(blk []models.Kline1min) decimal.Decimal {
	h := blk[0].High
	for _, k := range blk {
		if k.High.GreaterThan(h) {
			h = k.High
		}
	}
	return h
}
func blksLow(blk []models.Kline1min) decimal.Decimal {
	l := blk[0].Low
	for _, k := range blk {
		if k.Low.LessThan(l) {
			l = k.Low
		}
	}
	return l
}

// PredictK 预测接口：输出未来 N 根 1 分钟K线的涨跌方向。
// 基于行情引擎机械生成规则前向复算（控盘目标价 / floatStep 趋势延续 / 随机游走），
// 每根给出方向、预测收盘、置信度与驱动来源。默认预测后两根（n=2）。
func PredictK(c *gin.Context) {
	pid, _ := strconv.Atoi(c.Query("productId"))
	if pid <= 0 {
		utils.BadRequest(c, "参数错误：productId")
		return
	}
	n := 2
	if v := c.Query("n"); v != "" {
		if num, err := strconv.Atoi(v); err == nil && num > 0 {
			n = num
		}
	}
	res, err := market.PredictNext(uint(pid), n)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.OK(c, res)
}

// MyWallet 我的资金
func MyWallet(c *gin.Context) {
	user := middlewareGetUser(c)
	var w models.Wallet
	database.DB.Where("user_id = ?", user.ID).First(&w)
	utils.OK(c, w)
}

// WalletLogs 资金流水
func WalletLogs(c *gin.Context) {
	user := middlewareGetUser(c)
	page, size := utils.Page(c)
	var total int64
	q := database.DB.Model(&models.WalletLog{}).Where("user_id = ?", user.ID)
	q.Count(&total)
	var list []models.WalletLog
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// OpenOrderReq 下单请求
type OpenOrderReq struct {
	ProductID  uint   `json:"productId" binding:"required"`
	Side       int    `json:"side" binding:"required"` // 1买涨 2买跌
	OrderType  int    `json:"orderType"`               // 1市价 2限价
	Volume     int    `json:"volume" binding:"required"`
	Price      string `json:"price"` // 限价单价格（市价单可空）
	StopLoss   string `json:"stopLoss"`
	TakeProfit string `json:"takeProfit"`
}

// DoOpenOrder 开仓
func DoOpenOrder(c *gin.Context) {
	// 先读原始 body，兼容 price 为数字或字符串
	body, _ := io.ReadAll(c.Request.Body)
	_ = c.Request.Body.Close()

	type flex struct {
		ProductID     uint        `json:"productId"`
		Side          int         `json:"side"`
		OrderType     int         `json:"orderType"`
		Volume        int         `json:"volume"`
		Price         interface{} `json:"price"`
		StopLoss      interface{} `json:"stopLoss"`
		TakeProfit    interface{} `json:"takeProfit"`
		ClientOrderID string      `json:"clientOrderId"` // 客户端幂等键（可选；防重复下单）
	}
	var f flex
	if err := json.Unmarshal(body, &f); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	toDec := func(v interface{}) decimal.Decimal {
		switch t := v.(type) {
		case float64:
			return decimal.NewFromFloat(t)
		case string:
			d, _ := decimal.NewFromString(t)
			return d
		}
		return decimal.Zero
	}
	user := middlewareGetUser(c)
	if !requireRealName(c, user) {
		return
	}
	orderType := f.OrderType
	if orderType == 0 {
		orderType = 1
	}
	var cid *string
	if f.ClientOrderID != "" {
		cid = &f.ClientOrderID
	}
	order, pos, err := trader.OpenOrder(user, f.ProductID, f.Side, orderType, f.Volume,
		toDec(f.Price), toDec(f.StopLoss), toDec(f.TakeProfit), cid)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.OK(c, gin.H{"order": order, "position": pos})
}

// CancelOrder 撤单
func CancelOrder(c *gin.Context) {
	user := middlewareGetUser(c)
	id := c.Param("id")
	var oid uint
	if v, err := strconv.Atoi(id); err == nil {
		oid = uint(v)
	}
	if err := trader.CancelOrder(user.ID, oid); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.OKMsg(c, "撤单成功", nil)
}

// MyPositions 我的持仓（批量取品种消除 N+1，走单条 Redis 缓存）
func MyPositions(c *gin.Context) {
	user := middlewareGetUser(c)
	var list []models.Position
	database.DB.Where("user_id = ? AND status = 1", user.ID).Order("id desc").Find(&list)
	// 批量取品种（消除 N+1）
	prodIDs := make([]uint, 0, len(list))
	prodSet := map[uint]bool{}
	for _, p := range list {
		if !prodSet[p.ProductID] {
			prodIDs = append(prodIDs, p.ProductID)
			prodSet[p.ProductID] = true
		}
	}
	prodMap := batchProducts(prodIDs)
	// 附加品种与浮盈
	type VO struct {
		models.Position
		ProductName string          `json:"productName"`
		Profit      decimal.Decimal `json:"profit"`
	}
	out := make([]VO, 0, len(list))
	for _, p := range list {
		var prod models.Product
		if pp, ok := prodMap[p.ProductID]; ok {
			prod = pp
		}
		price := market.GetPrice(p.ProductID)
		unit := prod.UnitValue
		if unit.IsZero() {
			unit = decimal.NewFromInt(1)
		}
		diff := price.Sub(p.OpenPrice)
		if p.Side == 2 {
			diff = diff.Neg()
		}
		profit := diff.Mul(unit).Mul(decimal.NewFromInt(int64(p.Volume)))
		out = append(out, VO{Position: p, ProductName: prod.Name, Profit: profit})
	}
	utils.OK(c, out)
}

// PositionClose 手动平仓
func PositionClose(c *gin.Context) {
	user := middlewareGetUser(c)
	if !requireRealName(c, user) {
		return
	}
	var req struct {
		PositionID int `json:"positionId" binding:"required"`
		Volume     int `json:"volume"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	pos, err := trader.ClosePosition(user.ID, req.PositionID, req.Volume, false)
	_ = pos
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}
	utils.OKMsg(c, "平仓成功", nil)
}

// MyOrders 我的历史订单
func MyOrders(c *gin.Context) {
	user := middlewareGetUser(c)
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Order{}).Where("user_id = ?", user.ID)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.Order
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// ApplyDeposit 申请充值
func ApplyDeposit(c *gin.Context) {
	user := middlewareGetUser(c)
	var req struct {
		Amount  string `json:"amount" binding:"required"`
		Channel string `json:"channel"`
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
	if !channelDepositEnabled(req.Channel) {
		utils.BadRequest(c, "该充值渠道未开启")
		return
	}
	if err := database.DB.Create(&models.Deposit{
		UserID: user.ID, Amount: amount, Channel: req.Channel, Status: 0,
	}).Error; err != nil {
		utils.ServerError(c, "提交失败")
		return
	}
	utils.OKMsg(c, "充值申请已提交", nil)
}

// ApplyWithdraw 申请提现
func ApplyWithdraw(c *gin.Context) {
	user := middlewareGetUser(c)
	if !requireRealName(c, user) {
		return
	}
	var req struct {
		Amount  string `json:"amount" binding:"required"`
		Channel string `json:"channel"`
		Account string `json:"account" binding:"required"`
		Network string `json:"network"` // USDT 所属网络；出金不再要求姓名
		FundPwd string `json:"fundPwd"` // 与 /trader 一致：提现需资金密码
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
	if !channelWithdrawEnabled(req.Channel) {
		utils.BadRequest(c, "该出金渠道未开启")
		return
	}
	// 资金密码校验（对齐 /trader，防未设/差错密码出金）
	if user.FundPwd == "" {
		utils.BadRequest(c, "请先设置资金密码")
		return
	}
	if !database.CheckPwd(user.FundPwd, req.FundPwd) {
		utils.BadRequest(c, "资金密码错误")
		return
	}
	// 余额校验+冻结（原子增量：balance -= amount, frozen += amount）
	if _, ok := trader.AdjustWallet(user.ID, amount.Neg(), amount, "withdraw", amount.Neg(), "提现申请冻结"); !ok {
		utils.BadRequest(c, "余额不足")
		return
	}
	if err := database.DB.Create(&models.Withdraw{
		UserID: user.ID, Amount: amount, Channel: req.Channel, Account: req.Account, Network: req.Network, Status: 0,
	}).Error; err != nil {
		// 建单失败回滚冻结：归还余额并释放 frozen，避免"余额扣了、冻结悬挂"的幽灵资金
		trader.AdjustWallet(user.ID, amount, amount.Neg(), "withdraw_rollback", amount, "提现申请失败回滚")
		utils.ServerError(c, "提交失败")
		return
	}
	utils.OKMsg(c, "提现申请已提交", nil)
}

// jsonID 读取路径 id 参数（通用）
func jsonID(c *gin.Context, key string) uint {
	v := c.Param(key)
	if n, err := strconv.Atoi(v); err == nil {
		return uint(n)
	}
	return 0
}

// MarketBook 实时盘口（真实挂单 + 虚拟补位 + 控盘注入）
func MarketBook(c *gin.Context) {
	pid, _ := strconv.Atoi(c.Query("productId"))
	depth := 5
	if v := c.Query("depth"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			depth = n
		}
	}
	book := market.GetBook(uint(pid), depth)
	utils.OK(c, book)
}

// MarketTime 服务器时间与交易状态（前端显示开/休市）
func MarketTime(c *gin.Context) {
	start, end := market.TradingSession()
	utils.OK(c, gin.H{
		"serverTime":  time.Now().Format("2006-01-02 15:04:05"),
		"tradingOpen": market.TradingOpen(),
		"freeze":      market.Freezed(),
		"mode":        market.Mode(),
		"startHour":   start / 60,
		"endHour":     end / 60,
		"startTime":   market.MinutesToStr(start),
		"endTime":     market.MinutesToStr(end),
		"trading24h":  start == 0 && end == 0,
	})
}

// UserVerify 用户提交实名认证
func UserVerify(c *gin.Context) {
	user := middlewareGetUser(c)
	var req struct {
		RealName string `json:"realName" binding:"required"`
		IDCard   string `json:"idCard" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请填写姓名和身份证号")
		return
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).
		Updates(map[string]interface{}{
			"real_name": req.RealName, "id_card": req.IDCard,
			"real_name_remark": "", "real_name_status": 1, // 待后台审核，防自证实名绕过（与 TraderRealname 一致）
		}).Error; err != nil {
		utils.ServerError(c, "认证失败")
		return
	}
	cache.InvalidateUser(user.ID)
	utils.OKMsg(c, "已提交实名认证，等待审核", nil)
}
