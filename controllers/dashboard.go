package controllers

import (
	"time"

	"github.com/gin-gonic/gin"

	"server/database"
	"server/models"
	"server/utils"
)

// Dashboard 首页统计
func Dashboard(c *gin.Context) {
	var adminCount, roleCount, menuCount, logCount int64
	database.DB.Model(&models.Admin{}).Count(&adminCount)
	database.DB.Model(&models.Role{}).Count(&roleCount)
	database.DB.Model(&models.Menu{}).Where("type = ?", "C").Count(&menuCount)
	database.DB.Model(&models.OperationLog{}).Count(&logCount)

	// 最近 7 天操作日志量
	labels := make([]string, 0, 7)
	values := make([]int64, 0, 7)
	now := time.Now()
	for i := 6; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
		end := start.AddDate(0, 0, 1)
		var n int64
		database.DB.Model(&models.OperationLog{}).
			Where("created_at >= ? AND created_at < ?", start, end).Count(&n)
		labels = append(labels, start.Format("01-02"))
		values = append(values, n)
	}

	// 最近操作日志
	recentLogs := make([]models.OperationLog, 0)
	database.DB.Order("id desc").Limit(8).Find(&recentLogs)

	utils.OK(c, gin.H{
		"stats": gin.H{
			"adminCount": adminCount,
			"roleCount":  roleCount,
			"menuCount":  menuCount,
			"logCount":   logCount,
		},
		"trend": gin.H{
			"labels": labels,
			"values": values,
		},
		"recentLogs": recentLogs,
	})
}