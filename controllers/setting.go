package controllers

import (
	"strings"

	"github.com/gin-gonic/gin"

	"server/database"
	"server/models"
	"server/services/trader"
	"server/utils"
)

// settingValue 读取单个系统设置值（未找到返回 def）
func settingValue(key, def string) string {
	var s models.Setting
	if err := database.DB.Where("`key` = ?", key).First(&s).Error; err != nil {
		return def
	}
	return s.Value
}

// payFundEnabled 出入金渠道是否开启（key 形如 depositBank / withdrawUsdt）
func payFundEnabled(key string) bool {
	return settingValue(key, "1") == "1"
}

// needRealNameForce 是否需要实名认证（0=无需实名，注册即默认实名）
func needRealNameForce() bool {
	return settingValue("needRealName", "1") == "1"
}

// needMailCode 注册是否需要邮箱验证码（邮箱设置组 mailVerify，1开启 0关闭；默认开启）
func needMailCode() bool {
	return settingValue("mailVerify", "1") == "1"
}

// tradeSettingsHiddenKeys 交易设置分组里不应在「系统设置」重复展示的 key：
// 强平线 / 交易时段 / 控盘策略由「控盘管理」页面通过独立接口(/trade/strong-close、/trade/control/*、
// /trade/settings)读写，这里仅是行情引擎持久化的镜像副本，系统设置页展示会造成重复配置入口。
// 系统设置只保留其中的撮合类开关（如 systemMakeup 系统兜底）。
var tradeSettingsHiddenKeys = map[string]bool{
	"strong_close":     true,
	"trading_hours":    true,
	"control_strategy": true,
}

// Settings 返回全部分组的设置（按 groupCode 分组）
func Settings(c *gin.Context) {
	var list []models.Setting
	database.DB.Order("id asc").Find(&list)

	grouped := make(map[string][]models.Setting)
	for _, s := range list {
		// 过滤掉归控盘管理维护的交易类镜像配置，避免与系统设置重复
		if s.GroupCode == "trade" && tradeSettingsHiddenKeys[s.Key] {
			continue
		}
		grouped[s.GroupCode] = append(grouped[s.GroupCode], s)
	}
	// 组信息
	groupMeta := map[string]string{
		"website": "网站设置",
		"oss":     "OSS 存储",
		"email":   "邮箱设置",
		"sms":     "短信设置",
		"payfund": "出入金设置",
		"trade":   "交易设置",
	}

	utils.OK(c, gin.H{
		"groups": grouped,
		"meta":   groupMeta,
	})
}

// SettingSave 批量保存设置
func SettingSave(c *gin.Context) {
	var req struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	for _, item := range req.Items {
		if item.Key == "" {
			continue
		}
		database.DB.Model(&models.Setting{}).
			Where("`key` = ?", item.Key).
			Update("value", item.Value)
	}
	// 交易类设置变更后即时生效（本会话内撮合模式切换）
	trader.InvalidateMakeupCache()
	utils.OKMsg(c, "保存成功", nil)
}

// TestEmail 发送测试邮件，用于验证已保存的邮箱 SMTP 配置是否可用
func TestEmail(c *gin.Context) {
	var req struct {
		To string `json:"to"`
	}
	_ = c.ShouldBindJSON(&req)
	req.To = strings.TrimSpace(req.To)
	if req.To == "" || !emailRegex.MatchString(req.To) {
		utils.BadRequest(c, "请填写正确的测试收件邮箱")
		return
	}
	if err := sendEmailSMTP(req.To, "邮箱配置测试", "这是一封来自系统的测试邮件：收到即表示已保存的 SMTP 邮箱配置正确可用。"); err != nil {
		utils.ServerError(c, "邮件发送失败，请检查邮箱设置："+err.Error())
		return
	}
	utils.OKMsg(c, "邮件发送成功，请检查收件箱", nil)
}