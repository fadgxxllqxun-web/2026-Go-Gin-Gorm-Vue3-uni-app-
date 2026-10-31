package controllers

import (
	"time"

	"github.com/gin-gonic/gin"

	"server/cache"
	"server/database"
	"server/models"
	"server/utils"
)

// RegisterReq 注册
type RegisterReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Phone    string `json:"phone"`
	AgentCode uint   `json:"agentCode"` // 代理商用户编码（可选）
}

// UserRegister C端用户注册
func UserRegister(c *gin.Context) {
	var req RegisterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if len(req.Username) < 4 || len(req.Password) < 6 {
		utils.BadRequest(c, "用户名至少4位，密码至少6位")
		return
	}
	var count int64
	database.DB.Model(&models.User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		utils.BadRequest(c, "用户名已存在")
		return
	}
	hash, err := database.Hash(req.Password)
	if err != nil {
		utils.ServerError(c, "加密失败")
		return
	}
	user := models.User{
		Username: req.Username, Password: hash, Phone: req.Phone,
		Status: 1, Verified: 0,
	}
	// 系统关闭实名门槛时：注册即默认视为已实名
	if !needRealNameForce() {
		user.RealNameStatus = 2
		user.Verified = 1
	}
	// 绑定代理商
	if req.AgentCode > 0 {
		var agent models.Agent
		if err := database.DB.First(&agent, req.AgentCode).Error; err == nil && agent.Status == 1 {
			user.AgentID = agent.ID
		}
	}
	if err := database.DB.Create(&user).Error; err != nil {
		utils.ServerError(c, "注册失败")
		return
	}
	// 初始化资金账户
	database.DB.Create(&models.Wallet{UserID: user.ID, Balance: utils.Zero, Frozen: utils.Zero})
	// 用户注册：失效统计看板（用户数变更）
	cache.InvalidateStats()
	token, _ := utils.SignUserToken(user.ID, user.Username)
	user.Password, user.FundPwd = "", "" // 输出层清空敏感哈希
	utils.OK(c, gin.H{"token": token, "user": user})
}

// UserLogin C端用户登录
func UserLogin(c *gin.Context) {
	var req LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请输入账号和密码")
		return
	}
	var user models.User
	if err := database.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		utils.BadRequest(c, "账号不存在")
		return
	}
	if user.Status != 1 {
		utils.BadRequest(c, "该账号已被禁用")
		return
	}
	if !database.CheckPwd(user.Password, req.Password) {
		utils.BadRequest(c, "密码错误")
		return
	}
	now := time.Now()
	database.DB.Model(&user).Update("last_login", now)
	token, err := utils.SignUserToken(user.ID, user.Username)
	if err != nil {
		utils.ServerError(c, "生成令牌失败")
		return
	}
	user.Password, user.FundPwd = "", "" // 输出层清空敏感哈希
	utils.OK(c, gin.H{"token": token, "user": user})
}

// UserInfo C端用户信息
func UserInfo(c *gin.Context) {
	user := middlewareGetUser(c)
	// 加载资金账户
	var w models.Wallet
	database.DB.Where("user_id = ?", user.ID).First(&w)
	user.Password, user.FundPwd = "", ""             // 输出层清空敏感哈希
	user.IDFront, user.IDBack = "", ""               // 身份证 base64 图不需回传给 C 端（展示用本地上传预览），顺带减小响应
	utils.OK(c, gin.H{
		"user":   user,
		"wallet": w,
	})
}

// middlewareGetUser 复用中间件取用户（避免循环引用引入）
func middlewareGetUser(c *gin.Context) *models.User {
	v, _ := c.Get("user")
	u, _ := v.(models.User)
	return &u
}

// UserChangePassword C端用户修改密码
func UserChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"oldPassword" binding:"required"`
		NewPassword string `json:"newPassword" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	user := middlewareGetUser(c)
	if !database.CheckPwd(user.Password, req.OldPassword) {
		utils.BadRequest(c, "原密码错误")
		return
	}
	hash, _ := database.Hash(req.NewPassword)
	database.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("password", hash)
	utils.OKMsg(c, "修改成功", nil)
}

// SetFundPassword 设置资金密码
func SetFundPassword(c *gin.Context) {
	var req struct {
		FundPwd string `json:"fundPwd" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	user := middlewareGetUser(c)
	hash, _ := database.Hash(req.FundPwd)
	database.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("fund_pwd", hash)
	utils.OKMsg(c, "设置成功", nil)
}