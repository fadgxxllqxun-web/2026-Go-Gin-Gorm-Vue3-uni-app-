package controllers

import (
	"github.com/gin-gonic/gin"

	"server/cache"
	"server/database"
	"server/models"
	"server/utils"
)

// PermTree 权限管理：返回全部菜单/按钮树（含权限标识）
func PermTree(c *gin.Context) {
	utils.OK(c, database.GetAllMenuTree())
}

// PermList 权限标识列表（扁平，含类型字段）
func PermList(c *gin.Context) {
	var list []models.Menu
	database.DB.Order("sort asc").Find(&list)
	utils.OK(c, list)
}

// PermAssign 给角色授权（等价于 RoleAssignMenus，供权限管理页调用）
func PermAssign(c *gin.Context) {
	var req struct {
		RoleID  uint   `json:"roleId" binding:"required"`
		MenuIDs []uint `json:"menuIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var role models.Role
	if err := database.DB.First(&role, req.RoleID).Error; err != nil {
		utils.BadRequest(c, "角色不存在")
		return
	}
	if role.IsSuper == 1 {
		utils.OKMsg(c, "超级角色默认拥有全部权限", nil)
		return
	}
	database.DB.Where("role_id = ?", req.RoleID).Delete(&models.RoleMenu{})
	if len(req.MenuIDs) > 0 {
		rows := make([]models.RoleMenu, 0, len(req.MenuIDs))
		for _, mid := range req.MenuIDs {
			rows = append(rows, models.RoleMenu{RoleID: req.RoleID, MenuID: mid})
		}
		database.DB.Create(&rows)
	}
	// 角色-菜单绑定变更：失效权限码缓存（鉴权 Perm 热路径立即生效）
	cache.InvalidatePerms()
	utils.OKMsg(c, "授权成功", nil)
}