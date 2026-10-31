package controllers

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"server/database"
	"server/models"
	"server/utils"
)

// LogList 操作日志分页查询
func LogList(c *gin.Context) {
	page, size := utils.Page(c)
	keyword := c.Query("keyword")
	module := c.Query("module")
	status := c.Query("status")

	q := database.DB.Model(&models.OperationLog{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("admin_name LIKE ? OR path LIKE ? OR action LIKE ?", like, like, like)
	}
	if module != "" {
		q = q.Where("module = ?", module)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	q.Count(&total)
	var list []models.OperationLog
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// LogDelete 删除单条日志
func LogDelete(c *gin.Context) {
	id := c.Param("id")
	database.DB.Delete(&models.OperationLog{}, id)
	utils.OKMsg(c, "删除成功", nil)
}

// LogClear 清空日志
func LogClear(c *gin.Context) {
	database.DB.Where("1 = 1").Delete(&models.OperationLog{})
	utils.OKMsg(c, "清空成功", nil)
}

// LogModules 日志模块列表（去重）
func LogModules(c *gin.Context) {
	modules := make([]string, 0)
	database.DB.Model(&models.OperationLog{}).
		Where("module <> ''").Distinct("module").Order("module asc").Pluck("module", &modules)
	utils.OK(c, modules)
}

// strToUint 兼容辅助
func parseID(c *gin.Context, name string) uint {
	v, _ := strconv.ParseUint(c.Param(name), 10, 32)
	return uint(v)
}