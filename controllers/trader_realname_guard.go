package controllers

import (
	"github.com/gin-gonic/gin"

	"server/models"
)

// requireRealName 交易前置校验：用户必须实名认证通过（RealNameStatus==2）才能进行
// 下单/撤单/平仓/出入金等资金交易类操作，否则返回 700 并附原因。
// 返回 true 表示放行，false 表示已写响应、调用方应直接 return。
func requireRealName(c *gin.Context, u *models.User) bool {
	if u == nil {
		c.JSON(401, gin.H{"code": 401, "msg": "未登录", "data": nil})
		return false
	}
	// 兼容旧数据：Verified=1 且 RealNameStatus 未设视为已通过（与 traderUser 展示逻辑一致）
	if u.RealNameStatus == 2 || (u.Verified == 1 && u.RealNameStatus == 0) {
		return true
	}
	switch u.RealNameStatus {
	case 0:
		c.JSON(200, gin.H{"code": 700, "msg": "请先完成实名认证后再交易", "data": nil})
	case 1:
		c.JSON(200, gin.H{"code": 700, "msg": "实名认证审核中，请等待审核通过后再交易", "data": nil})
	case 3:
		c.JSON(200, gin.H{"code": 700, "msg": "实名认证被驳回，请重新提交", "data": nil})
	default:
		c.JSON(200, gin.H{"code": 700, "msg": "实名认证状态异常，暂不可交易", "data": nil})
	}
	return false
}
