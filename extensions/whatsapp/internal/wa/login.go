package wa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
)

// Login steps for dual-mode WhatsApp pairing:
//   - stepMethod: choose QR scan ("qr") or 8-digit phone pairing code (enter phone with country code).
//   - stepQR:     displays a scannable WhatsApp QR code (PromptQRCode), polled every 2s until paired.
//   - stepCode:   displays an 8-digit pairing code (PromptDeviceCode), polled every 2s until paired.
const (
	stepMethod = "method"
	stepQR     = "qr"
	stepCode   = "pair_code"

	loginPollInterval = 2
	loginMaxAge       = 3 * time.Minute
	qrFirstWait       = 15 * time.Second
	pairHelpURL       = "https://faq.whatsapp.com/1317564962315842"
)

const methodHint = "支持两种方式关联你的 WhatsApp 账号（多设备协议，登录态与密钥加密保存在 hub）：\n" +
	"1. 单手机推荐：输入带国家区号的手机号（如 +12025550123 或 +86 开头）获取 8 位配对码；\n" +
	"2. 扫码登录：输入 qr 获取二维码，用手机 WhatsApp 扫描。"

type pendingLogin struct {
	mode    string // stepQR | stepCode
	session LoginSession
	started time.Time

	mu      sync.Mutex
	qrCode  string
	code    string
	paired  bool
	account string
	jid     string
	lid     string
	err     error
}

func (p *pendingLogin) watch() {
	for evt := range p.session.Events() {
		p.mu.Lock()
		if evt.QRCode != "" {
			p.qrCode = evt.QRCode
		}
		if evt.Paired {
			p.paired = true
			p.account = evt.Account
			p.jid = evt.JID
			p.lid = evt.LID
		}
		if evt.Err != nil {
			p.err = evt.Err
		}
		p.mu.Unlock()
		if evt.Paired || evt.Err != nil {
			return
		}
	}
}

func (p *pendingLogin) snapshot() (qrCode, code string, paired bool, account, jid, lid string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.qrCode, p.code, p.paired, p.account, p.jid, p.lid, p.err
}

func methodPrompt(problem string) connector.Prompt {
	return connector.Prompt{
		Step:  stepMethod,
		Hint:  methodHint,
		Error: problem,
		Fields: []connector.Field{
			{
				Name:        "method",
				Label:       "手机号或 qr",
				Placeholder: "+1 202 555 0123 或 qr",
			},
		},
	}
}

func qrPrompt(qr string) connector.Prompt {
	return connector.Prompt{
		Step:     stepQR,
		Kind:     connector.PromptQRCode,
		URL:      qr,
		Interval: loginPollInterval,
		Hint:     "打开手机 WhatsApp → 设置 → 已关联设备 → 关联设备，扫描上方二维码。二维码过期会自动刷新。",
	}
}

func pairCodePrompt(code string) connector.Prompt {
	return connector.Prompt{
		Step:     stepCode,
		Kind:     connector.PromptDeviceCode,
		URL:      pairHelpURL,
		UserCode: code,
		Interval: loginPollInterval,
		Hint:     "打开手机 WhatsApp → 设置 → 已关联设备 → 关联设备 → 改用手机号码关联，输入上方 8 位配对码（约 2 分钟内有效）。",
	}
}

// LoginStep advances the dual-mode (QR + 8-digit phone pairing code) WhatsApp sign-in flow.
func (c *Conn) LoginStep(ctx context.Context, step string, values map[string]string) (connector.Prompt, error) {
	if c.backend == nil {
		return connector.Prompt{}, errors.New("WhatsApp backend is not initialized")
	}
	switch step {
	case "":
		// Already signed in?
		if st := c.Status(ctx); st.State == connector.StateOK && st.Account != "" {
			return connector.Prompt{Done: true, Account: st.Account}, nil
		}
		c.cancelPendingLogin()
		return methodPrompt(""), nil

	case stepMethod:
		raw := strings.TrimSpace(firstNonEmpty(values["method"], values["phone"], values["mode"]))
		if raw == "" {
			return methodPrompt("请输入带国家区号的手机号（获取 8 位配对码），或输入 qr 获取二维码"), nil
		}
		if isQRChoice(raw) {
			return c.startQRLogin(ctx)
		}
		digits, err := NormalizePhoneNumber(raw)
		if err != nil {
			return methodPrompt("格式不对：" + err.Error() + "；如需扫码请输入 qr"), nil
		}
		return c.startPairCodeLogin(ctx, digits)

	case stepQR:
		return c.pollPendingLogin(ctx, stepQR)

	case stepCode:
		return c.pollPendingLogin(ctx, stepCode)

	default:
		c.cancelPendingLogin()
		return methodPrompt(""), nil
	}
}

func isQRChoice(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "qr", "qrcode", "qr-code", "qr_code", "scan", "扫码", "二维码":
		return true
	default:
		return false
	}
}

func (c *Conn) startQRLogin(ctx context.Context) (connector.Prompt, error) {
	c.cancelPendingLogin()
	sess, err := c.backend.StartLogin(ctx)
	if err != nil {
		return methodPrompt("连不上 WhatsApp，请检查网络或代理设置后重试（" + scrubError(err) + "）"), nil
	}
	p := &pendingLogin{mode: stepQR, session: sess, started: c.now()}
	go p.watch()

	// Wait for the first QR payload (or immediate error/pair).
	deadline := time.NewTimer(qrFirstWait)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		qr, _, paired, acc, jid, lid, perr := p.snapshot()
		if paired {
			return c.finishLogin(acc, jid, lid), nil
		}
		if perr != nil {
			sess.Cancel()
			return methodPrompt("获取二维码失败：" + scrubError(perr)), nil
		}
		if qr != "" {
			c.loginMu.Lock()
			c.pending = p
			c.loginMu.Unlock()
			return qrPrompt(qr), nil
		}
		select {
		case <-ctx.Done():
			sess.Cancel()
			return connector.Prompt{}, ctx.Err()
		case <-deadline.C:
			sess.Cancel()
			return methodPrompt("等待 WhatsApp 返回二维码超时，请检查网络或代理设置后重试"), nil
		case <-ticker.C:
		}
	}
}

func (c *Conn) startPairCodeLogin(ctx context.Context, digits string) (connector.Prompt, error) {
	c.cancelPendingLogin()
	sess, err := c.backend.StartLogin(ctx)
	if err != nil {
		return methodPrompt("连不上 WhatsApp，请检查网络或代理设置后重试（" + scrubError(err) + "）"), nil
	}
	p := &pendingLogin{mode: stepCode, session: sess, started: c.now()}
	go p.watch()

	code, err := sess.RequestPairCode(ctx, digits)
	if err != nil {
		sess.Cancel()
		return methodPrompt("申请 8 位配对码失败：" + scrubError(err) + "（请确认手机号含正确国家区号，或输入 qr 扫码登录）"), nil
	}
	p.mu.Lock()
	p.code = FormatPairingCode(code)
	displayCode := p.code
	p.mu.Unlock()

	c.loginMu.Lock()
	c.pending = p
	c.loginMu.Unlock()
	return pairCodePrompt(displayCode), nil
}

func (c *Conn) pollPendingLogin(ctx context.Context, wantMode string) (connector.Prompt, error) {
	c.loginMu.Lock()
	p := c.pending
	c.loginMu.Unlock()

	if p == nil || p.mode != wantMode {
		// If already logged in on another request, report done.
		if st := c.Status(ctx); st.State == connector.StateOK && st.Account != "" {
			return connector.Prompt{Done: true, Account: st.Account}, nil
		}
		return methodPrompt("本次登录会话已结束，请重新选择扫码或手机号配对码"), nil
	}

	qr, code, paired, acc, jid, lid, perr := p.snapshot()
	if paired {
		c.clearPendingIf(p)
		return c.finishLogin(acc, jid, lid), nil
	}
	if perr != nil {
		c.clearPendingIf(p)
		p.session.Cancel()
		return methodPrompt(fmt.Sprintf("%s，请重新输入手机号或 qr", scrubError(perr))), nil
	}
	if c.now().Sub(p.started) > loginMaxAge {
		c.clearPendingIf(p)
		p.session.Cancel()
		return methodPrompt("等待配对已超时，请重新输入手机号或 qr"), nil
	}
	if wantMode == stepQR {
		if qr == "" {
			return methodPrompt("二维码尚未就绪，请重新输入 qr"), nil
		}
		return qrPrompt(qr), nil
	}
	return pairCodePrompt(code), nil
}

func (c *Conn) finishLogin(account, jid, lid string) connector.Prompt {
	if account == "" {
		account = BestDisplayName("", "", "", jid)
	}
	if jid != "" && c.store != nil {
		_ = c.store.ResetForAccount(jid)
		if lid != "" {
			_ = c.store.PutLIDMapping(lid, jid)
		}
	}
	c.mu.Lock()
	c.selfJID = jid
	c.selfLID = lid
	c.status = connector.Status{
		State:   connector.StateOK,
		Account: account,
		Via:     c.viaLabel(),
	}
	c.mu.Unlock()
	// Wake Run() so the persistent live client connects with the newly persisted encrypted device state.
	c.kick()
	return connector.Prompt{Done: true, Account: account}
}

func (c *Conn) clearPendingIf(p *pendingLogin) {
	c.loginMu.Lock()
	if c.pending == p {
		c.pending = nil
	}
	c.loginMu.Unlock()
}

func (c *Conn) cancelPendingLogin() {
	c.loginMu.Lock()
	p := c.pending
	c.pending = nil
	c.loginMu.Unlock()
	if p != nil && p.session != nil {
		p.session.Cancel()
	}
}

// Logout unlinks the WhatsApp companion device (when reachable), wipes the encrypted Hub secret
// and local search index, and transitions the connector back to StateNotSignedIn.
func (c *Conn) Logout(ctx context.Context) error {
	c.cancelPendingLogin()
	var err error
	if c.backend != nil {
		err = c.backend.Logout(ctx)
	}
	if c.store != nil {
		_ = c.store.Wipe()
	}
	c.mu.Lock()
	c.selfJID, c.selfLID = "", ""
	c.lastSync = time.Time{}
	c.lastEvent = time.Time{}
	c.status = connector.Status{
		State: connector.StateNotSignedIn,
		Error: "还没登录 WhatsApp（支持二维码扫码或手机号 8 位配对码）",
	}
	c.mu.Unlock()
	c.kick()
	return err
}
