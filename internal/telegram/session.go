package telegram

import "context"

func (a *App) SessionHealth() error { return a.sessions.Health() }

func (a *App) sessionOK(ctx context.Context, chatID int64, err error) bool {
	if err == nil {
		return true
	}
	// Do not log connection URLs, encrypted payloads or receiving details.
	a.logger.Warn("session action not committed")
	a.send(ctx, chatID, "会话保存暂时不可用或已被其他操作更新，本次操作已停止。请勿重复提交提现，请先核对提现记录。\nSession storage is unavailable or changed. Action stopped; check withdrawal history before submitting again.", nil)
	return false
}
