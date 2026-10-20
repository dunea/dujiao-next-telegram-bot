package operations

import "fmt"

// Human summaries are deliberately separate from stable machine audit codes.
func actionLabel(action Action) string {
	switch action {
	case Order:
		return "创建订单"
	case Payment:
		return "创建支付"
	case OrderCancel:
		return "取消订单"
	case Recharge:
		return "创建充值"
	case GiftCard:
		return "兑换礼品卡"
	case Affiliate:
		return "开通推广"
	case Withdrawal:
		return "申请提现"
	case WithdrawalUnlock:
		return "解除提现保护"
	case CallbackPaid:
		return "付款成功通知"
	case CallbackFulfilled:
		return "交付完成通知"
	case CallbackRecharge:
		return "充值到账通知"
	case Session:
		return "会话存储检查"
	case Access:
		return "访问权限检查"
	case Callback:
		return "接收商店回调"
	case AdminTest:
		return "管理员通知测试"
	case BroadcastDraft:
		return "公告草稿"
	case BroadcastJob:
		return "公告群发任务"
	case BroadcastDelivery:
		return "公告发送"
	case Subscription:
		return "公告订阅"
	case DeliveryTXT:
		return "交付文件领取"
	default:
		return "待核对操作"
	}
}

func outcomeLabel(outcome Outcome) string {
	switch outcome {
	case Attempt:
		return "正在处理"
	case Succeeded:
		return "已完成"
	case Rejected:
		return "未通过"
	case Unknown:
		return "结果待核对，请勿重复提交"
	case Blocked:
		return "已阻止"
	case Limited:
		return "操作过于频繁，请稍后再试"
	case Unavailable:
		return "暂时不可用"
	case Paused:
		return "已暂停"
	case Cancelled:
		return "已取消"
	case Skipped:
		return "已跳过"
	case Subscribed:
		return "已订阅"
	case Unsubscribed:
		return "已退订"
	default:
		return "待核对"
	}
}

func adminNotification(event Event) string {
	result := outcomeLabel(event.Outcome)
	if event.Outcome == Succeeded {
		switch event.Action {
		case Order:
			result = "订单已创建（不代表已付款或已发货）"
		case CallbackPaid, CallbackFulfilled, CallbackRecharge:
			result = "已向买家发送通知"
		case Withdrawal:
			result = "申请已提交，等待商家审核（不代表已打款）"
		}
	}
	text := fmt.Sprintf("🔔 管理员通知\n事件：%s\n结果：%s", actionLabel(event.Action), result)
	if event.UserID > 0 {
		text += fmt.Sprintf("\n用户 ID：%d", event.UserID)
	}
	if event.ResourceID > 0 {
		label := "关联编号"
		switch event.Action {
		case Order, OrderCancel, CallbackPaid, CallbackFulfilled, DeliveryTXT:
			label = "订单 ID"
		}
		text += fmt.Sprintf("\n%s：%d", label, event.ResourceID)
	}
	if event.id != "" {
		text += "\n诊断编号：" + event.id
	}
	if event.Action == Withdrawal && event.Outcome != Succeeded {
		text += "\n请核对商店提现记录；勿自动重试资金操作。"
	}
	return text
}
