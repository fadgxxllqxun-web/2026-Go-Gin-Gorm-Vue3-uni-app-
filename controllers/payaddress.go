package controllers

import (
	"sync"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/database"
	"server/models"
	"server/utils"
)

// payAddrMu 轮询配发在同一 DB 进程内串行化，避免并发拿到重复地址
var payAddrMu sync.Mutex

// ---------- 后台：收款地址管理（支付宝/银行卡/USDT） ----------

// PaymentAddressList 收款地址列表（支持按分类/启用状态筛选）
func PaymentAddressList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.PaymentAddress{})
	if v := c.Query("category"); v != "" {
		q = q.Where("category = ?", v)
	}
	if v := c.Query("enabled"); v != "" {
		q = q.Where("enabled = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.PaymentAddress
	q.Order("category asc, id asc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// PaymentAddressAll 收款地址全量列表（无需分页，供管理页直接展示）
func PaymentAddressAll(c *gin.Context) {
	var list []models.PaymentAddress
	database.DB.Order("category asc, id asc").Find(&list)
	utils.OK(c, list)
}

// PaymentAddressSave 新增/编辑收款地址
func PaymentAddressSave(c *gin.Context) {
	var req struct {
		ID       uint   `json:"id"`
		Category string `json:"category" binding:"required"`
		Name     string `json:"name"`
		Account  string `json:"account" binding:"required"`
		Network  string `json:"network"`
		BankName string `json:"bankName"`
		Enabled  int    `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	switch req.Category {
	case "alipay", "bank", "usdt":
	default:
		utils.BadRequest(c, "分类不合法（仅支持 alipay/bank/usdt）")
		return
	}
	if req.Enabled != 0 {
		req.Enabled = 1
	}

	if req.ID == 0 {
		pa := models.PaymentAddress{
			Category: req.Category, Name: req.Name, Account: req.Account,
			Network: req.Network, BankName: req.BankName, Enabled: req.Enabled,
		}
		if err := database.DB.Create(&pa).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		utils.OKMsg(c, "新增成功", pa)
		return
	}

	var pa models.PaymentAddress
	if err := database.DB.First(&pa, req.ID).Error; err != nil {
		utils.BadRequest(c, "地址不存在")
		return
	}
	pa.Category = req.Category
	pa.Name = req.Name
	pa.Account = req.Account
	pa.Network = req.Network
	pa.BankName = req.BankName
	pa.Enabled = req.Enabled
	// 展示次数/收款金额不在编辑界面直接改，避免误改统计；用单独接口归零
	if err := database.DB.Save(&pa).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	utils.OKMsg(c, "更新成功", nil)
}

// PaymentAddressDelete 删除收款地址
func PaymentAddressDelete(c *gin.Context) {
	id := c.Param("id")
	if id == "" || id == "0" {
		utils.BadRequest(c, "参数错误")
		return
	}
	database.DB.Delete(&models.PaymentAddress{}, id)
	utils.OKMsg(c, "删除成功", nil)
}

// PaymentAddressResetStats 归零某地址的展示次数/收款金额
func PaymentAddressResetStats(c *gin.Context) {
	var req struct {
		ID uint `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if err := database.DB.Model(&models.PaymentAddress{}).Where("id = ?", req.ID).
		Updates(map[string]interface{}{"show_count": 0, "received_amount": 0}).Error; err != nil {
		utils.ServerError(c, "重置失败")
		return
	}
	utils.OKMsg(c, "已重置", nil)
}

// ---------- 出金/入金：渠道配置与收款地址配发 ----------

// payChannelMap 渠道显示名（前端展示用）
var payChannelMap = map[string]string{
	"bank":   "银行卡",
	"alipay": "支付宝",
	"usdt":   "USDT",
}

// TraderPaymentConfig 出入金配置（前端据此渲染可用的充值/出金渠道 + 是否强制实名）
func TraderPaymentConfig(c *gin.Context) {
	depositCh := make([]gin.H, 0)
	for _, ch := range []string{"bank", "alipay", "usdt"} {
		if payFundEnabled("deposit" + upperFirst(ch)) {
			depositCh = append(depositCh, gin.H{"value": ch, "label": payChannelMap[ch]})
		}
	}
	withdrawCh := make([]gin.H, 0)
	for _, ch := range []string{"bank", "alipay", "usdt"} {
		if payFundEnabled("withdraw" + upperFirst(ch)) {
			withdrawCh = append(withdrawCh, gin.H{"value": ch, "label": payChannelMap[ch]})
		}
	}
	traderOK(c, gin.H{
		"depositEnabled":  depositCh,
		"withdrawEnabled": withdrawCh,
		"needRealName":    needRealNameForce(),
		"needMailCode":    needMailCode(), // 注册是否需要邮箱验证码
	})
}

// TraderPaymentAddress 按渠道轮询配发一个启用中的收款地址（充值展示）。
// 轮询策略：同渠道内已展示次数最少者优先（均等轮转），命中后 ShowCount+1。
func TraderPaymentAddress(c *gin.Context) {
	category := c.Query("category")
	if category == "" {
		traderOK(c, nil)
		return
	}
	// 加进程内互斥，避免并发换取到同一地址
	payAddrMu.Lock()
	defer payAddrMu.Unlock()

	var pa models.PaymentAddress
	if err := database.DB.
		Where("category = ? AND enabled = 1", category).
		Order("show_count asc, id asc").
		First(&pa).Error; err != nil {
		traderOK(c, nil) // 该渠道暂未配置收款地址
		return
	}
	// 展示次数 +1（原子增量）
	database.DB.Model(&models.PaymentAddress{}).Where("id = ?", pa.ID).
		UpdateColumn("show_count", gorm.Expr("show_count + 1"))

	traderOK(c, gin.H{
		"id":       pa.ID,
		"category": pa.Category,
		"name":     pa.Name,
		"account":  pa.Account,
		"network":  pa.Network,
		"bankName": pa.BankName,
	})
}

// upperFirst 首字母大写（channel -> Channel，拼 system 设置 key 用）
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// channelDepositEnabled 充值渠道是否开启。
// bank/alipay/usdt 走后台开关；其它渠道（如老客户端的 wechat/other）没有开关，默认视为开启，
// 避免影响存量客户端。
func channelDepositEnabled(ch string) bool {
	return payFundEnabled("deposit" + upperFirst(ch))
}

// channelWithdrawEnabled 出金渠道是否开启（规则同上）
func channelWithdrawEnabled(ch string) bool {
	return payFundEnabled("withdraw" + upperFirst(ch))
}