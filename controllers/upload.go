package controllers

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"server/config"
	"server/database"
	"server/models"
	"server/utils"
)

// Upload 通用文件上传，返回可访问的完整 URL
func Upload(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		utils.BadRequest(c, "未选择文件")
		return
	}
	dir := config.Cfg.Upload.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		utils.ServerError(c, "目录创建失败")
		return
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext == "" {
		ext = ".png"
	}
	// 仅允许常见的图片/文档类型（不含 svg，避免同源 /static 下发含脚本的 SVG 造成存储型 XSS）
	allowed := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".ico": true, ".pdf": true, ".doc": true, ".docx": true, ".xlsx": true}
	if !allowed[ext] {
		utils.BadRequest(c, "不支持的文件类型")
		return
	}
	if file.Size > 10<<20 { // 10MB 上限，防匿名/超限填充磁盘
		utils.BadRequest(c, "文件过大，最大 10MB")
		return
	}

	name := fmt.Sprintf("%d_%s%s", time.Now().UnixNano(), randStr(6), ext)
	dst := filepath.Join(dir, name)
	if err := c.SaveUploadedFile(file, dst); err != nil {
		utils.ServerError(c, "保存失败")
		return
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s/static/%s", scheme, c.Request.Host, name)
	utils.OK(c, gin.H{
		"url":  url,
		"path": "/static/" + name,
		"name": file.Filename,
	})
}

// PublicSiteInfo 公开的网站信息（登录页 / 顶部品牌使用，无需登录）
func PublicSiteInfo(c *gin.Context) {
	var list []models.Setting
	database.DB.Where("group_code = ?", "website").Find(&list)
	kv := map[string]string{}
	for _, s := range list {
		kv[s.Key] = s.Value
	}
	utils.OK(c, gin.H{
		"siteName":  kv["siteName"],
		"siteLogo":  kv["siteLogo"],
		"siteUrl":   kv["siteUrl"],
		"version":   kv["version"],
		"copyright": kv["copyright"],
		"icp":       kv["icp"],
	})
}

const letters = "abcdefghijklmnopqrstuvwxyz0123456789"

func randStr(n int) string {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}