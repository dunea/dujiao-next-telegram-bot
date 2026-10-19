// Package compatibility provides bounded, sanitized, GET-only upgrade diagnostics.
package compatibility

import (
	"context"
	"errors"
	"net/http"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type Check struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status,omitempty"`
}
type Report struct {
	Schema          int     `json:"schema"`
	UpstreamVersion string  `json:"upstream_version"`
	ConfigRevision  int     `json:"config_revision"`
	ReadChecksOK    bool    `json:"required_read_checks_ok"`
	WriteChecks     string  `json:"write_checks"`
	Checks          []Check `json:"checks"`
}

// Probe does not infer an upstream release from its mutable config revision.
// The caller should use dujiao.NewReadOnlyClient as a second line of protection.
func Probe(ctx context.Context, client *dujiao.Client, userID string) Report {
	report := Report{Schema: 1, UpstreamVersion: "unknown", ReadChecksOK: true, WriteChecks: "not_run", Checks: []Check{}}
	add := func(name string, required bool, err error) {
		check := Check{Name: name, Required: required, Status: "accepted"}
		if err != nil {
			check.Status = "unavailable"
			if errors.Is(err, dujiao.ErrInvalidReadResponse) {
				check.Status = "invalid_response"
			}
			var api *dujiao.APIError
			if errors.As(err, &api) {
				check.HTTPStatus = api.HTTPStatus
				switch api.HTTPStatus {
				case http.StatusNotFound, http.StatusNotImplemented:
					check.Status = "missing"
				case http.StatusUnauthorized, http.StatusForbidden:
					check.Status = "denied"
				case http.StatusTooManyRequests:
					check.Status = "throttled"
				default:
					if api.HTTPStatus < 500 {
						check.Status = "rejected"
					}
				}
			}
		}
		if required && check.Status != "accepted" {
			report.ReadChecksOK = false
		}
		report.Checks = append(report.Checks, check)
	}
	config, err := client.BotConfig(ctx)
	add("telegram.config", true, err)
	if err == nil {
		report.ConfigRevision = config.ConfigVersion
		if !config.Config.Enabled {
			report.Checks[0].Status = "disabled_or_incomplete"
			report.ReadChecksOK = false
		}
	}
	// Do not continue after an authentication/configuration failure.
	if !report.ReadChecksOK {
		return report
	}
	_, err = client.Categories(ctx, "zh-CN")
	add("catalog.categories", true, err)
	_, err = client.RechargeChannels(ctx)
	add("wallet.recharge_channels", false, err)
	if userID != "" {
		_, err = client.Wallet(ctx, userID)
		add("wallet.balance", false, err)
		_, err = client.WalletTransactions(ctx, userID, 1, 1)
		add("wallet.transactions", false, err)
		_, err = client.AffiliateDashboard(ctx, userID)
		add("affiliate.dashboard", false, err)
		_, err = client.AffiliateCommissions(ctx, userID, 1)
		add("affiliate.commissions", false, err)
		_, err = client.AffiliateWithdrawals(ctx, userID, 1)
		add("affiliate.withdrawals", false, err)
	}
	return report
}
