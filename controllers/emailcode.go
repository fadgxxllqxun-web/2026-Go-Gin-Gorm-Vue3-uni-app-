package controllers

import (
	"crypto/tls"
	"fmt"
	"math/rand"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"server/cache"
	"server/database"
	"server/models"
)

// 邮箱验证码有效期（5 分钟）
const verifyCodeTTL = 5 * time.Minute

// emailCfg 从系统设置「邮箱设置」组读取 SMTP 配置（host/port/username/password/from/ssl）
func emailCfg() (host string, port int, user, pass, from string, ssl bool) {
	get := func(k, def string) string {
		var s models.Setting
		if err := database.DB.Where("group_code = ? AND `key` = ?", "email", k).First(&s).Error; err == nil {
			return s.Value
		}
		return def
	}
	host = get("host", "")
	port, _ = strconv.Atoi(get("port", "465"))
	user = get("username", "")
	pass = get("password", "")
	from = get("from", "")
	ssl = get("ssl", "true") == "true"
	if user == "" {
		user = from
	}
	return
}

// sendEmailSMTP 发送文本邮件（SSL/465 或 STARTTLS/587）
func sendEmailSMTP(to, subject, body string) error {
	host, port, user, pass, from, ssl := emailCfg()
	if host == "" {
		return fmt.Errorf("SMTP 未配置")
	}
	if from == "" {
		from = user
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		from, to, subject, body)

	if ssl || port == 465 {
		// 隐式 TLS（465）
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, InsecureSkipVerify: true})
		if err != nil {
			return err
		}
		defer conn.Close()
		cl, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer cl.Close()
		return smtpSend(cl, user, pass, host, from, to, msg)
	}
	// 587 STARTTLS
	cl, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer cl.Close()
	if ok, _ := cl.Extension("STARTTLS"); ok {
		if err := cl.StartTLS(&tls.Config{ServerName: host, InsecureSkipVerify: true}); err != nil {
			return err
		}
	}
	return smtpSend(cl, user, pass, host, from, to, msg)
}

func smtpSend(cl *smtp.Client, user, pass, host, from, to, msg string) error {
	if user != "" {
		if err := cl.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(from); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	return w.Close()
}

// verifyCodeKey 验证码缓存 key（按邮箱+场景）
func verifyCodeKey(email, scene string) string { return "cache:verifycode:" + scene + ":" + email }

// genVerifyCode 生成 6 位数字验证码
func genVerifyCode() string { return fmt.Sprintf("%06d", rand.Intn(1000000)) }

// TraderSendMailCode 发送邮箱验证码（场景：register 等；需系统设置里配好邮箱 SMTP）
func TraderSendMailCode(c *gin.Context) {
	var req struct {
		Email string `json:"email"`
		Scene string `json:"scene"`
	}
	_ = c.ShouldBindJSON(&req)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || !emailRegex.MatchString(req.Email) {
		c.JSON(200, gin.H{"code": 1, "msg": "邮箱格式不正确", "data": nil})
		return
	}
	if req.Scene == "" {
		req.Scene = "register"
	}
	// 后台关闭「邮箱验证」时，发送验证码无意义，直接提示未开启
	if !needMailCode() {
		c.JSON(200, gin.H{"code": 1, "msg": "邮箱验证功能未开启", "data": nil})
		return
	}
	// 注册场景：邮箱已注册则不允许重复发送
	if req.Scene == "register" {
		var cnt int64
		database.DB.Model(&models.User{}).
			Where("username = ? OR email = ?", req.Email, req.Email).Count(&cnt)
		if cnt > 0 {
			c.JSON(200, gin.H{"code": 1, "msg": "该邮箱已注册", "data": nil})
			return
		}
	}

	code := genVerifyCode()
	cache.Set(verifyCodeKey(req.Email, req.Scene), code, verifyCodeTTL)
	body := fmt.Sprintf("您的邮箱验证码是：%s，5 分钟内有效。若非本人操作请忽略本邮件。", code)

	res := gin.H{"msg": "验证码已发送"}
	// SMTP 未配置（开发模式）：回显验证码供本地调试直接使用
	host, _, _, _, _, _ := emailCfg()
	if host == "" {
		res["devCode"] = code
	} else if err := sendEmailSMTP(req.Email, "邮箱验证码", body); err != nil {
		// 已配置邮箱但发送失败：返回真实错误，避免误显示为开发模式
		c.JSON(200, gin.H{"code": 1, "msg": "邮件发送失败，请检查邮箱设置：" + err.Error(), "data": nil})
		return
	}
	traderOK(c, res)
}

// verifyEmailCode 校验邮箱验证码（校验后即失效，防重放）
func verifyEmailCode(email, scene, code string) bool {
	if email == "" || code == "" {
		return false
	}
	email = strings.TrimSpace(strings.ToLower(email))
	got, ok, _ := cache.Get[string](verifyCodeKey(email, scene))
	if !ok || got == "" || code != got {
		return false
	}
	cache.Del(verifyCodeKey(email, scene))
	return true
}