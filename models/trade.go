package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ---------- C端用户 ----------

// User C端交易用户
type User struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	Username    string         `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Nickname    string         `gorm:"size:64" json:"nickname"`
	Password    string         `gorm:"size:255;not null" json:"password,omitempty"`
	Phone       string         `gorm:"size:16;index" json:"phone"`
	Email       string         `gorm:"size:128" json:"email"`
	RealName    string         `gorm:"size:64" json:"realName"`
	IDCard      string         `gorm:"size:18" json:"idCard"`
	Avatar      string         `gorm:"size:255" json:"avatar"`
	FundPwd     string         `gorm:"size:255" json:"fundPwd,omitempty"` // 资金密码（哈希；序列化可见以便缓存回填，输出层已置空）
	AgentID     uint           `gorm:"default:0;index" json:"agentId"` // 所属代理商
	Status      int            `gorm:"default:1" json:"status"`        // 1启用 0禁用
	Verified    int            `gorm:"default:0" json:"verified"`      // 0未实名 1已实名
	RealNameStatus int        `gorm:"default:0" json:"realNameStatus"` // 实名状态:0未提交 1审核中 2已通过 3已驳回
	RealNameRemark string    `gorm:"type:text" json:"realNameRemark"`  // 实名审核备注（驳回原因）
	IDFront     string         `gorm:"type:mediumtext" json:"idFront"` // 身份证正面图片(base64 dataURL，可能超过64KB)
	IDBack      string         `gorm:"type:mediumtext" json:"idBack"`  // 身份证反面图片(base64 dataURL，可能超过64KB)
	LastLogin   *time.Time     `json:"lastLogin"`
	Wallet      *Wallet        `gorm:"foreignKey:UserID" json:"wallet,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// ---------- 资金 ----------

// Wallet 用户资金账户
type Wallet struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	UserID    uint            `gorm:"uniqueIndex;not null" json:"userId"`
	Balance   decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"balance"`   // 可用余额
	Frozen    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"frozen"`    // 冻结金额
	Debt      decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"debt"`     // 挂账负债（平台垫付的手续费/费用，正数）
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// WalletLog 资金流水
type WalletLog struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	UserID    uint            `gorm:"index" json:"userId"`
	Type      string          `gorm:"size:32;index" json:"type"`      // deposit/withdraw/trade_fee/open/close/adjust/rebate
	Amount    decimal.Decimal `gorm:"type:decimal(18,4)" json:"amount"`  // 正数加 负数减
	Balance   decimal.Decimal `gorm:"type:decimal(18,4)" json:"balance"` // 变动后余额
	Remark    string          `gorm:"size:255" json:"remark"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ---------- 交易品种 ----------

// Product 交易品种
type Product struct {
	ID           uint            `gorm:"primaryKey" json:"id"`
	Name         string          `gorm:"size:64;not null" json:"name"`
	Code         string          `gorm:"size:32;uniqueIndex;not null" json:"code"`
	TickSize     decimal.Decimal `gorm:"type:decimal(10,4);not null" json:"tickSize"` // 最小变动价位
	MarginRate   decimal.Decimal `gorm:"type:decimal(8,6);not null" json:"marginRate"` // 保证金比例(每单位)
	FixedMargin  decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"fixedMargin"` // 固定保证金(每手，元)：>0 时 1手=该固定费用，与实时价格/杠杆无关；0=按价格计算
	UnitValue    decimal.Decimal `gorm:"type:decimal(18,4);not null;default:1" json:"unitValue"` // 每手价格单位
	FeePerHand   decimal.Decimal `gorm:"type:decimal(10,4);default:0" json:"feePerHand"` // 每手手续费
	FeeRate      decimal.Decimal `gorm:"type:decimal(8,6);default:0" json:"feeRate"`    // 按金额手续费率
	FloatStep    decimal.Decimal `gorm:"type:decimal(12,4);default:0" json:"floatStep"` // 每根K线收盘固定浮动量（0=使用随机游走）
	DayFloatRate decimal.Decimal `gorm:"type:decimal(10,4);default:0" json:"dayFloatRate"` // 每日浮动比例（如0.2=20%），当日涨跌上下边界
	WickLength   decimal.Decimal `gorm:"type:decimal(10,4);default:0" json:"wickLength"` // 每根K线影线长度（最高=实体上沿+该值、最低=实体下沿−该值；0=沿用 浮动/2）
	PrevClose    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"prevClose"`   // 昨日收盘价（当日涨跌基准及涨跌幅基准）
	MinVolume    int             `gorm:"default:1" json:"minVolume"`
	MaxVolume    int             `gorm:"default:100" json:"maxVolume"`
	MaxPosition  int             `gorm:"default:500" json:"maxPosition"`
	Leverage     int             `gorm:"default:10" json:"leverage"` // 杠杆倍数：保证金=价×Unit×手数×保证金率/倍数，盈亏=(现价-开仓价)×Unit×手数/倍数
	LastPrice    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"lastPrice"` // 最新价
	OpenPrice    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"openPrice"` // 今开
	Status       int             `gorm:"default:1" json:"status"` // 1交易中 0停盘
	AntiFlat     int             `gorm:"default:1" json:"antiFlat"` // 防横盘 1开启(默认) 0关闭：行情长时间窄幅横盘时强制放一段单边走趋势
	Sort         int             `gorm:"default:0" json:"sort"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// 计算兜底倍数：品种未配置杠杆时按 10（防 0 除零）。
func (p *Product) LeverageOr10() int {
	if p.Leverage <= 0 {
		return 10
	}
	return p.Leverage
}

// ---------- 订单 ----------

// OrderStatus 订单状态（旧 int 枚举：0待成交 2已成交 3已撤单）
type OrderStatus int

const (
	OrderPending    OrderStatus = 0 // 待成交：下单创建，等待撮合
	OrderHolding    OrderStatus = 2 // 已成交：开仓成交（是否仍持有看 positions）
	OrderCompleted  OrderStatus = 2 // 已成交：平仓单（订单管理按动作各记一条）
	OrderCanceled   OrderStatus = 3 // 已撤单：用户/系统撤单
	OrderLiquidated OrderStatus = 2 // 已成交：强平单
)

// Order 订单
type Order struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	OrderNo    string          `gorm:"size:32;uniqueIndex" json:"orderNo"`
	UserID     uint            `gorm:"index" json:"userId"`
	ProductID  uint            `gorm:"index" json:"productId"`
	Side       int             `gorm:"index" json:"side"`      // 1买涨 2买跌
	OrderType  int             `json:"orderType"`              // 1市价 2限价
	Price      decimal.Decimal `gorm:"type:decimal(18,4)" json:"price"`
	Volume     int             `json:"volume"`
	Fee        decimal.Decimal `gorm:"type:decimal(18,4)" json:"fee"`
	Margin     decimal.Decimal `gorm:"type:decimal(18,4)" json:"margin"`
	StopLoss   decimal.Decimal `gorm:"type:decimal(18,4)" json:"stopLoss"`
	TakeProfit decimal.Decimal `gorm:"type:decimal(18,4)" json:"takeProfit"`
	Status     OrderStatus     `gorm:"index;default:0" json:"status"` // 0待成交 2已成交 3已撤单
	Filled     int             `gorm:"default:0" json:"filled"`
	MatchType  int             `gorm:"default:1" json:"matchType"` // 1 A-Book内部撮合（系统已无B-Book承接，默认即A-Book）
	ClosePrice decimal.Decimal `gorm:"type:decimal(18,4)" json:"closePrice"`
	IsClose    int             `gorm:"default:0" json:"isClose"` // 0开仓 1平仓
	PositionID uint            `gorm:"default:0" json:"positionId"`
	// OpenOrderID 平仓订单关联的开仓(买入)订单ID：买入订单成交后，卖出平仓单回填其来源订单ID，
	// 后台订单列表据此把平仓单与前面那笔买入单挂上钩（仅平仓订单 IsClose=1 有意义）。
	OpenOrderID uint            `gorm:"default:0;index" json:"openOrderId"`
	Profit      decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"profit"` // 平仓盈亏（仅平仓订单 IsClose=1 有意义）
	// ClientOrderID 客户端幂等键（可空）。下单端每次提交生成唯一值，后端据其防重复下单/重复冻结；
	// NULL(未传)不参与唯一约束，兼容旧客户端。
	ClientOrderID *string `gorm:"size:64;unique;default:NULL" json:"clientOrderId"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ---------- 持仓 ----------

// Position 持仓
type Position struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	UserID     uint            `gorm:"index" json:"userId"`
	ProductID  uint            `gorm:"index" json:"productId"`
	Side       int             `gorm:"index" json:"side"`
	Volume     int             `json:"volume"`
	OpenPrice  decimal.Decimal `gorm:"type:decimal(18,4)" json:"openPrice"`
	Margin     decimal.Decimal `gorm:"type:decimal(18,4)" json:"margin"`
	TotalFee   decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"totalFee"`
	StopLoss   decimal.Decimal `gorm:"type:decimal(18,4)" json:"stopLoss"`
	TakeProfit decimal.Decimal `gorm:"type:decimal(18,4)" json:"takeProfit"`
	Status     int             `gorm:"default:1" json:"status"` // 1持仓中 2已平仓
	ClosePrice decimal.Decimal `gorm:"type:decimal(18,4)" json:"closePrice"`
	Profit     decimal.Decimal `gorm:"type:decimal(18,4)" json:"profit"` // 平仓后盈亏
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	ClosedAt   *time.Time      `json:"closedAt"`
}

// ---------- 出入金 ----------

// Deposit 充值
type Deposit struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	UserID     uint            `gorm:"index" json:"userId"`
	Amount     decimal.Decimal `gorm:"type:decimal(18,4)" json:"amount"`
	Channel    string          `gorm:"size:32" json:"channel"` // 支付渠道 bank/alipay/usdt
	AddressID  uint            `gorm:"default:0;index" json:"addressId"` // 关联的收款地址（前端展示的平台收款地址；无需付款人姓名/账号）
	AccountName string         `gorm:"size:64" json:"accountName"` // 收款账户姓名
	AccountNo  string          `gorm:"size:128" json:"accountNo"`   // 收款账号
	Screenshot string          `gorm:"type:text" json:"screenshot"` // 转账截图URL
	Status     int             `gorm:"default:0" json:"status"` // 0待审核 1通过 2拒绝
	Remark     string          `gorm:"size:255" json:"remark"`
	AuditBy    uint            `json:"auditBy"`
	AuditAt    *time.Time      `json:"auditAt"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// ---------- 收款地址管理 ----------

// PaymentAddress 平台收款地址（支付宝/银行卡/USDT 三类）。
// - 前端充值时按渠道拉取地址：Go 后端做轮询（按已展示次数最少优先），并自增 ShowCount；
// - 列表展示 ShowCount（被展示次数）与 ReceivedAmount（该地址到账累计，充单审核通过时累加）。
type PaymentAddress struct {
	ID             uint            `gorm:"primaryKey" json:"id"`
	Category       string          `gorm:"size:16;index" json:"category"` // alipay / bank / usdt
	Name           string          `gorm:"size:128" json:"name"`         // 收款户名/收款人/备注
	Account        string          `gorm:"size:255" json:"account"`      // 账号/卡号/USDT 收款地址
	Network        string          `gorm:"size:32" json:"network"`       // USDT 所属网络：TRC20/ERC20/OMNI 等（其它渠道可空）
	BankName       string          `gorm:"size:128" json:"bankName"`     // 开户行（银行卡渠道）
	Enabled        int             `gorm:"default:1" json:"enabled"`     // 0停用 1启用
	ShowCount      int64           `gorm:"default:0" json:"showCount"`   // 被展示/配发次数
	ReceivedAmount decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"receivedAmount"` // 该地址累计收款金额
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// Withdraw 提现
type Withdraw struct {
	ID       uint            `gorm:"primaryKey" json:"id"`
	UserID   uint            `gorm:"index" json:"userId"`
	Amount   decimal.Decimal `gorm:"type:decimal(18,4)" json:"amount"`
	Channel  string          `gorm:"size:32" json:"channel"` // 收款渠道 bank/alipay/usdt
	Account  string          `gorm:"size:128" json:"account"` // 收款账号/地址
	Network  string          `gorm:"size:32" json:"network"`  // USDT 所属网络（TRC20/ERC20 等；无需姓名）
	Name     string          `gorm:"size:64" json:"name"`     // 收款人姓名（兼容旧字段，新流程不再填写）
	Screenshot string        `gorm:"type:text" json:"screenshot"` // 收款账户截图URL
	Status   int             `gorm:"default:0" json:"status"` // 0待审核 1通过 2拒绝
	Remark   string          `gorm:"size:255" json:"remark"`
	AuditBy  uint            `json:"auditBy"`
	AuditAt  *time.Time      `json:"auditAt"`
	CreatedAt time.Time      `json:"createdAt"`
}

// ---------- 代理商 ----------

// Agent 代理商
type Agent struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	AgentName string `gorm:"size:64;not null" json:"agentName"`
	Phone     string `gorm:"size:20" json:"phone"`  // 手机号
	Wechat    string `gorm:"size:64" json:"wechat"` // 微信号
	QQ        string `gorm:"size:32" json:"qq"`     // QQ号
	Username  string `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Password      string          `gorm:"size:255;not null" json:"-"`
	InviteCode    string          `gorm:"size:8;uniqueIndex" json:"inviteCode"` // 邀请码：6位字母数字混合，唯一；注册时填写匹配
	ParentID      uint            `gorm:"default:0;index" json:"parentId"`
	Level         int             `gorm:"default:1" json:"level"`
	FeeShareRate  decimal.Decimal `gorm:"type:decimal(8,6);default:0" json:"feeShareRate"` // 手续费分润比例
	LossShareRate decimal.Decimal `gorm:"type:decimal(8,6);default:0" json:"lossShareRate"` // 头寸分润比例
	Balance       decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"balance"`      // 可提现余额
	FrozenBalance decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"frozenBalance"`
	Status        int             `gorm:"default:1" json:"status"`
	LastLogin     *time.Time      `json:"lastLogin"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// AgentWithdraw 代理商提现
type AgentWithdraw struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	AgentID   uint            `gorm:"index" json:"agentId"`
	Amount    decimal.Decimal `gorm:"type:decimal(18,4)" json:"amount"`
	Account   string          `gorm:"size:128" json:"account"`
	Name      string          `gorm:"size:64" json:"name"`
	Status    int             `gorm:"default:0" json:"status"` // 0待审核 1通过 2拒绝
	Remark    string          `gorm:"size:255" json:"remark"`
	AuditBy   uint            `json:"auditBy"`
	AuditAt   *time.Time      `json:"auditAt"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ProfitShare 分润流水
type ProfitShare struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	AgentID   uint            `gorm:"index" json:"agentId"`
	UserID    uint            `gorm:"uniqueIndex:uk_profit_share_action,priority:1" json:"userId"`
	ProductID uint            `json:"productId"`
	OrderID   uint            `json:"orderId"`
	// ActionType 分润对应的订单动作：open开仓(1) / close平仓(2) / loss亏损(3)；
	// 非订单动作类分润（代理上下分/额度划拨等）为 0，不参与幂等防重。
	// 与 user_id+order_id+type+idemp_key 组成复合唯一索引 uk_profit_share_action：
	// 非订单动作行四列全 0/空（MySQL 唯一索引对全 NULL/空值行互不冲突、天然放行），
	// 订单动作行凭幂等键全局唯一 —— 作为 DB 层最终兜底（跨进程/重启重放防重复入账）。
	ActionType uint   `gorm:"default:0;uniqueIndex:uk_profit_share_action,priority:3" json:"actionType"`
	// IdempKey 分润幂等键（业务层生成，如 "1102|fee|9289|open"）；空值仅出现在
	// 非订单动作类分润，配合上面的全放行特性，不会与订单动作行撞键。
	IdempKey  string          `gorm:"size:64;uniqueIndex:uk_profit_share_action,priority:4" json:"idempKey"`
	Type      string          `gorm:"size:32;uniqueIndex:uk_profit_share_action,priority:2" json:"type"` // fee手续费 / loss头寸
	Amount    decimal.Decimal `gorm:"type:decimal(18,4)" json:"amount"`
	Status    int             `gorm:"default:1" json:"status"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ---------- 控盘 ----------

// ControlRule 控盘规则
// 新语义（整段趋势引导）：在 [StartMinute, EndMinute] 窗口内，每根K线按产品 float_step
// 固定涨/跌一个浮动量，方向由引擎自由发挥 + 必要强迫，窗口结束累计净变幅 = NetPoints
// （涨为正、跌为负）。StartPrice 为窗口首根开盘价，首根命中时回填锁定起点。
type ControlRule struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	ProductID    uint       `gorm:"index" json:"productId"`
	Direction    int        `json:"direction"` // 1涨 2跌
	NetPoints    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"netPoints"` // 窗口内净变幅点数（有符号：涨+ 跌-）
	TargetPrice  decimal.Decimal `gorm:"type:decimal(18,4)" json:"targetPrice"`
	StartPrice   decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"startPrice"` // 窗口首根开盘价（首根命中时回填）
	FloatRange   decimal.Decimal `gorm:"type:decimal(10,4)" json:"floatRange"`
	StartMinute  time.Time  `json:"startMinute"`
	EndMinute    time.Time  `json:"endMinute"`
	NeedleHigh   decimal.Decimal `gorm:"type:decimal(18,4)" json:"needleHigh"`
	NeedleLow    decimal.Decimal `gorm:"type:decimal(18,4)" json:"needleLow"`
	Status       int       `gorm:"default:0" json:"status"` // 0待生效 1生效中 2已结束
	CreatedBy    uint      `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ---------- 行情 ----------

// Tick 秒级行情
type Tick struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	ProductID uint            `gorm:"index" json:"productId"`
	Price     decimal.Decimal `gorm:"type:decimal(18,4)" json:"price"`
	Seq       int             `json:"seq"` // 分钟序号 1-60
	TickTime  time.Time       `gorm:"index" json:"tickTime"`
}

// Kline1min 分钟K线
type Kline1min struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	ProductID uint            `gorm:"index:idx_pk_time" json:"productId"`
	Open      decimal.Decimal `gorm:"type:decimal(18,4)" json:"open"`
	High      decimal.Decimal `gorm:"type:decimal(18,4)" json:"high"`
	Low       decimal.Decimal `gorm:"type:decimal(18,4)" json:"low"`
	Close     decimal.Decimal `gorm:"type:decimal(18,4)" json:"close"`
	Volume    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"volume"` // 本分钟成交量
	Amount    decimal.Decimal `gorm:"type:decimal(18,4);default:0" json:"amount"` // 本分钟成交额
	Timestamp time.Time       `gorm:"index:idx_pk_time" json:"timestamp"`
}

// Notice 消息/公告（user_id=0 表示全局公告）
type Notice struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;default:0" json:"userId"` // 0=全员
	Title     string    `gorm:"size:128" json:"title"`
	Content   string    `gorm:"type:text" json:"content"`
	Type      string    `gorm:"size:32;index;default:bulletin" json:"type"` // bulletin/force_close/withdraw/deposit/risk/trade_close
	IsRead    int       `gorm:"default:0" json:"isRead"`
	CreatedAt time.Time `json:"createdAt"`
}

// Banner App 首页轮播图（管理端维护，公开接口给 C 端首页用）
type Banner struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Title     string    `gorm:"size:128" json:"title"`
	Image     string    `gorm:"size:255;not null" json:"image"` // 图片地址（/static/... 或完整 URL）
	Link      string    `gorm:"size:255" json:"link"`           // 跳转链接（可空）
	Sort      int       `gorm:"default:0;index" json:"sort"`
	Status    int       `gorm:"default:1;index" json:"status"` // 1 启用 0 停用
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}