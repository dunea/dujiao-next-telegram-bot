//go:build realdujiao

package realdujiao

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/compatibility"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type fixture struct {
	Key                    string `json:"channel_key"`
	Secret                 string `json:"channel_secret"`
	Product, SKU           uint
	Promoter               dujiao.AffiliateProfile
	Cases                  []businessCase
	InviteID, InviteUserID uint
}
type businessCase struct {
	User            dujiao.TelegramIdentity
	UserID          uint
	Card            string
	Paid, Cancelled dujiao.OrderCreated
	WithdrawalID    uint
}

func TestRealUpgrade(t *testing.T) {
	if os.Getenv("DUJIAO_REAL_TEST") != "1" {
		t.Fatal("explicit DUJIAO_REAL_TEST=1 required; these tests create synthetic Docker databases")
	}
	drivers := []string{"sqlite", "postgres"}
	if driver := os.Getenv("DUJIAO_REAL_DB"); driver != "" {
		if driver != "sqlite" && driver != "postgres" {
			t.Fatal("DUJIAO_REAL_DB must be sqlite or postgres")
		}
		drivers = []string{driver}
	}
	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			s := newStack(t, driver)
			var f fixture
			var markers string
			var previous, backupSnapshot string
			for i := range releases {
				if !t.Run(releases[i].version, func(t *testing.T) {
					s.t = t
					s.start(i, "upgrade")
					// SQLite must not be opened by macOS sqlite3 concurrently with
					// the Linux VM's writer over Docker Desktop bind mounts. Stop
					// after the real migration before inspecting its persistent DB.
					s.stop()
					if i > 0 && s.dataSnapshot() != previous {
						t.Fatal("persisted business rows changed during upgrade")
					}
					s.start(i, "business")
					if i == 0 {
						f = s.seed()
					} else {
						s.verify(f)
					}
					s.checkAuthentication(f)
					s.exercise(&f, i)
					if i == 2 {
						s.exerciseInvite(&f)
					}
					s.verify(f)
					s.stop()
					current := s.checkDatabase()
					s.checkSchema(i, f)
					if i > 0 && current != markers {
						t.Fatal("previous migration markers changed during version upgrade")
					}
					markers = current
					previous = s.dataSnapshot()
					if i == 0 {
						s.backup()
						backupSnapshot = previous
					}
					// A second startup executes the real upstream migration path again.
					s.start(i, "repeat")
					s.verify(f)
					s.stop()
					if s.checkDatabase() != markers {
						t.Fatal("migration markers changed on repeated startup")
					}
					if s.dataSnapshot() != previous {
						t.Fatal("business rows changed during repeat migration")
					}
					t.Log("PASS: authenticated reads, business writes, old data and migration idempotence")
				}) {
					s.t = t
					return
				}
			}
			s.t = t
			// Restore the pre-upgrade backup and start the old binary on that copy.
			// This is NOT an in-place downgrade of a migrated database.
			s.restore()
			s.start(0, "restored")
			f.Cases = f.Cases[:1]
			f.InviteID = 0
			f.InviteUserID = 0
			s.verify(f)
			s.stop()
			s.checkDatabase()
			s.checkSchema(0, f)
			if s.dataSnapshot() != backupSnapshot {
				t.Fatal("backup restore did not preserve exact business rows")
			}
			t.Log("PASS: pre-upgrade backup restored and verified with real v1.4.8")
		})
	}
}

func must[T any](t *testing.T, name string, value T, err error) T {
	t.Helper()
	if err != nil {
		var api *dujiao.APIError
		if errors.As(err, &api) {
			t.Fatalf("%s: HTTP %d, status %d, error_code %s", name, api.HTTPStatus, api.StatusCode, api.ErrorCode)
		}
		t.Fatalf("%s: client/transport failure (%T)", name, err)
	}
	return value
}

func (s *stack) channel(f fixture, readonly bool) *dujiao.Client {
	s.t.Helper()
	var c *dujiao.Client
	var err error
	if readonly {
		c, err = dujiao.NewReadOnlyClient(s.base, f.Key, f.Secret, 10*time.Second)
	} else {
		c, err = dujiao.NewClient(s.base, f.Key, f.Secret, 10*time.Second)
	}
	if err != nil {
		s.t.Fatal("invalid isolated client configuration")
	}
	return c
}

func (s *stack) seed() fixture {
	t := s.t
	token := s.login()
	var f fixture
	// No Bot token or callback URL: the backend is exercised through the real
	// channel API, not by running Telegram workers or callbacks to this Bot.
	s.admin("POST", "/channel-clients", token, map[string]any{"name": "isolated-real-test", "channel_type": "telegram_bot"}, &f)
	if f.Key == "" || f.Secret == "" {
		t.Fatal("channel credentials missing")
	}
	s.admin("PUT", "/settings/telegram-bot", token, map[string]any{"enabled": true, "default_locale": "zh-CN", "basic": map[string]any{"display_name": "Synthetic migration fixture"}}, nil)
	s.admin("PUT", "/settings/affiliate", token, map[string]any{"enabled": true, "commission_rate": 10, "confirm_days": 0, "min_withdraw_amount": 0.5, "withdraw_channels": []string{"test-only"}}, nil)
	var category struct {
		ID uint `json:"id"`
	}
	s.admin("POST", "/categories", token, map[string]any{"slug": "migration-fixture", "name": map[string]string{"zh-CN": "合成测试商品"}}, &category)
	var product struct {
		ID   uint `json:"id"`
		SKUs []struct {
			ID uint `json:"id"`
		} `json:"skus"`
	}
	s.admin("POST", "/products", token, map[string]any{"category_id": category.ID, "slug": "migration-item", "title": map[string]string{"zh-CN": "合成测试商品"}, "price_amount": 7.25, "fulfillment_type": "manual", "manual_stock_total": 100, "is_active": true, "is_affiliate_enabled": true}, &product)
	f.Product = product.ID
	c := s.channel(f, false)
	ctx := context.Background()
	detail, err := c.Product(ctx, "zh-CN", "", f.Product)
	detail = must(t, "product fixture", detail, err)
	if len(detail.SKUs) != 1 || detail.SKUs[0].ID == 0 {
		t.Fatal("default SKU missing")
	}
	f.SKU = detail.SKUs[0].ID
	profile, err := c.OpenAffiliate(ctx, dujiao.TelegramIdentity{ChannelUserID: "910000000", FirstName: "Synthetic promoter", Locale: "zh-CN"})
	f.Promoter = must(t, "open affiliate", profile, err)
	return f
}

func (s *stack) checkAuthentication(f fixture) {
	t := s.t
	bad := f
	bad.Secret = randomHex(32)
	_, err := s.channel(bad, false).BotConfig(context.Background())
	var api *dujiao.APIError
	if !errors.As(err, &api) || api.HTTPStatus != 401 {
		t.Fatal("invalid HMAC was not denied with HTTP 401")
	}
	r, err := s.client.Get(s.base + "/api/v1/channel/telegram/config")
	if err != nil {
		t.Fatal("unsigned probe failed")
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("unsigned request was not denied with HTTP 401")
	}
}

func (s *stack) exercise(f *fixture, i int) {
	t := s.t
	ctx := context.Background()
	c := s.channel(*f, false)
	b := businessCase{User: dujiao.TelegramIdentity{ChannelUserID: fmt.Sprintf("91000000%d", i+1), FirstName: "Synthetic buyer", Locale: "zh-CN"}}
	identity, err := c.ProvisionIdentity(ctx, b.User)
	identity = must(t, "provision", identity, err)
	if identity.UserID == 0 || !identity.Bound || !identity.Created {
		t.Fatal("provision did not create a bound identity")
	}
	b.UserID = identity.UserID
	again, err := c.ProvisionIdentity(ctx, b.User)
	again = must(t, "provision repeated", again, err)
	if again.UserID != b.UserID || again.Created {
		t.Fatal("provision created a duplicate identity")
	}
	token := s.login()
	s.admin("POST", "/gift-cards/generate", token, map[string]any{"name": "synthetic-" + releases[i].version, "quantity": 1, "amount": "100.00"}, nil)
	var cards []struct {
		Code string `json:"code"`
	}
	s.admin("GET", "/gift-cards?status=active", token, nil, &cards)
	if len(cards) != 1 || cards[0].Code == "" {
		t.Fatal("expected one unredeemed test gift card")
	}
	b.Card = cards[0].Code
	redeemed, err := c.RedeemGiftCard(ctx, b.User.ChannelUserID, b.Card)
	redeemed = must(t, "redeem", redeemed, err)
	if redeemed.WalletDelta != "100.00" || redeemed.Wallet.Balance != "100.00" {
		t.Fatal("gift-card wallet amount mismatch")
	}
	request := dujiao.OrderRequest{TelegramIdentity: b.User, Items: []dujiao.OrderItemRequest{{ProductID: f.Product, SKUID: f.SKU, Quantity: 1}}, AffiliateCode: f.Promoter.Code}
	preview, err := c.PreviewOrder(ctx, request)
	preview = must(t, "preview", preview, err)
	if !preview.Valid || preview.TotalAmount != "7.25" {
		t.Fatal("preview amount or validation mismatch")
	}
	order, err := c.CreateOrder(ctx, request)
	b.Cancelled = must(t, "create cancelled order", order, err)
	cancelled, err := c.CancelOrder(ctx, b.User.ChannelUserID, b.Cancelled.OrderID)
	cancelled = must(t, "cancel", cancelled, err)
	if cancelled.Status != "canceled" || cancelled.CancelledAt == "" {
		t.Fatal("order cancellation not acknowledged")
	}
	order, err = c.CreateOrder(ctx, request)
	b.Paid = must(t, "create paid order", order, err)
	paid, err := c.CreatePayment(ctx, b.User.ChannelUserID, b.Paid.OrderID, 0, true)
	paid = must(t, "wallet payment", paid, err)
	if !paid.OrderPaid {
		t.Fatal("wallet payment did not pay the order")
	}
	// A replay must not debit the wallet twice, irrespective of whether the
	// server returns an already-paid response or a business rejection.
	_, _ = c.CreatePayment(ctx, b.User.ChannelUserID, b.Paid.OrderID, 0, true)
	// Real paid-order commission (7.25 * 10% = 0.73), never a SQL-inserted
	// pretend balance. Only safe GETs are polled while the upstream queue settles.
	s.wait(func() bool {
		dashboard, err := c.AffiliateDashboard(ctx, "910000000")
		return err == nil && dashboard.AvailableCommission == cents((i+1)*73-i*50)
	})
	withdrawal, err := c.ApplyAffiliateWithdrawal(ctx, dujiao.AffiliateWithdrawalRequest{ChannelUserID: "910000000", Amount: "0.50", Channel: "test-only", Account: "synthetic-no-payout"})
	withdrawal = must(t, "real commission withdrawal application", withdrawal, err)
	if withdrawal.Status != "pending_review" {
		t.Fatal("withdrawal did not remain pending review")
	}
	b.WithdrawalID = withdrawal.ID
	f.Cases = append(f.Cases, b)
	// This applies for review only: no admin approval or payout API is called.
	_, err = c.ApplyAffiliateWithdrawal(ctx, dujiao.AffiliateWithdrawalRequest{ChannelUserID: "910000000", Amount: "1000000.00", Channel: "test-only", Account: "synthetic-no-payout"})
	var api *dujiao.APIError
	if !errors.As(err, &api) || api.HTTPStatus != 400 {
		t.Fatal("over-balance withdrawal not rejected")
	}
}

func (s *stack) verify(f fixture) {
	t := s.t
	ctx := context.Background()
	c := s.channel(f, false)
	config, err := c.BotConfig(ctx)
	config = must(t, "config", config, err)
	if !config.Config.Enabled || config.Config.Basic.DisplayName != "Synthetic migration fixture" {
		t.Fatal("Bot settings were not preserved")
	}
	categories, err := c.Categories(ctx, "zh-CN")
	categories = must(t, "categories", categories, err)
	if len(categories) != 1 || categories[0].Slug != "migration-fixture" {
		t.Fatal("category not preserved")
	}
	products, err := c.Products(ctx, "zh-CN", "", categories[0].ID, 1, 5)
	products = must(t, "products", products, err)
	if len(products.Items) != 1 || products.Items[0].ID != f.Product {
		t.Fatal("product not preserved")
	}
	detail, err := c.Product(ctx, "zh-CN", "", f.Product)
	detail = must(t, "product", detail, err)
	if len(detail.SKUs) != 1 || detail.SKUs[0].ID != f.SKU || detail.SKUs[0].Price != "7.25" {
		t.Fatal("SKU or price changed")
	}
	dashboard, err := c.AffiliateDashboard(ctx, "910000000")
	dashboard = must(t, "affiliate", dashboard, err)
	if !dashboard.Opened || dashboard.AffiliateCode != f.Promoter.Code || dashboard.MinWithdrawAmount.String() != "0.5" {
		t.Fatal("affiliate settings/profile not preserved")
	}
	if dashboard.AvailableCommission != cents(len(f.Cases)*23) {
		t.Fatalf("synthetic available commission mismatch: got %s, expected %s", dashboard.AvailableCommission, cents(len(f.Cases)*23))
	}
	withdrawals, err := c.AffiliateWithdrawals(ctx, "910000000", 1)
	withdrawals = must(t, "withdrawals", withdrawals, err)
	if withdrawals.Total != int64(len(f.Cases)) || len(withdrawals.Items) != len(f.Cases) {
		t.Fatal("withdrawals lost or duplicated")
	}
	for _, b := range f.Cases {
		found := false
		for _, w := range withdrawals.Items {
			if w.ID == b.WithdrawalID {
				found = true
				if w.Amount != "0.50" || w.Channel != "test-only" || w.Account != "synthetic-no-payout" || w.Status != "pending_review" {
					t.Fatal("withdrawal changed during upgrade")
				}
			}
		}
		if !found {
			t.Fatal("historical withdrawal ID missing")
		}
	}
	if f.InviteID != 0 {
		var codes []struct {
			ID   uint `json:"id"`
			Used int  `json:"used_count"`
		}
		s.admin("GET", "/invite-codes", s.login(), nil, &codes)
		if len(codes) != 1 || codes[0].ID != f.InviteID || codes[0].Used != 1 {
			t.Fatal("invite code usage not preserved")
		}
		identity, err := c.ResolveIdentity(ctx, dujiao.TelegramIdentity{ChannelUserID: "910000010"})
		identity = must(t, "invite identity", identity, err)
		if identity.UserID != f.InviteUserID || !identity.Bound {
			t.Fatal("invited identity not preserved")
		}
	}
	for _, b := range f.Cases {
		identity, err := c.ResolveIdentity(ctx, b.User)
		identity = must(t, "resolve preserved identity", identity, err)
		if identity.UserID != b.UserID || !identity.Bound {
			t.Fatal("identity binding changed")
		}
		wallet, err := c.Wallet(ctx, b.User.ChannelUserID)
		wallet = must(t, "wallet", wallet, err)
		if wallet.Balance != "92.75" || wallet.Currency != "CNY" {
			t.Fatal("wallet changed or duplicate debit occurred")
		}
		tx, err := c.WalletTransactions(ctx, b.User.ChannelUserID, 1, 20)
		tx = must(t, "transactions", tx, err)
		if len(tx.Items) != 2 || tx.Total != 2 {
			t.Fatal("wallet ledger changed or duplicated")
		}
		_, err = c.RedeemGiftCard(ctx, b.User.ChannelUserID, b.Card)
		var api *dujiao.APIError
		if !errors.As(err, &api) || api.HTTPStatus != 400 {
			t.Fatal("redeemed gift-card replay not rejected")
		}
		for _, expected := range []struct {
			order     dujiao.OrderCreated
			cancelled bool
		}{{b.Cancelled, true}, {b.Paid, false}} {
			order, err := c.Order(ctx, b.User.ChannelUserID, "zh-CN", expected.order.OrderID)
			order = must(t, "numeric order route", order, err)
			if order.OrderNo != expected.order.OrderNo || order.TotalAmount != "7.25" {
				t.Fatal("order data changed")
			}
			if expected.cancelled && (order.Status != "canceled" || order.CancelledAt == "") {
				t.Fatal("cancelled order status changed")
			}
			if !expected.cancelled && (order.WalletPaidAmount != "7.25" || order.PaidAmount != "7.25") {
				t.Fatal("paid order amount changed")
			}
		}
		orders, err := c.Orders(ctx, b.User.ChannelUserID, "zh-CN", "", 1, 20)
		orders = must(t, "orders", orders, err)
		if orders.Total != 2 || len(orders.Items) != 2 {
			t.Fatal("orders lost or duplicated")
		}
		report := compatibility.Probe(ctx, s.channel(f, true), b.User.ChannelUserID)
		if len(report.Checks) != 8 || !report.ReadChecksOK {
			t.Fatal("real read-only diagnostic incomplete")
		}
		for _, check := range report.Checks {
			if check.Status != "accepted" {
				t.Fatalf("real probe %s: %s", check.Name, check.Status)
			}
		}
	}
}

func cents(n int) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }

func (s *stack) exerciseInvite(f *fixture) {
	t := s.t
	token := s.login()
	c := s.channel(*f, false)
	ctx := context.Background()
	s.admin("PUT", "/settings", token, map[string]any{"key": "registration_config", "value": map[string]any{"registration_enabled": true, "invite_only_enabled": true}}, nil)
	identity := dujiao.TelegramIdentity{ChannelUserID: "910000010", FirstName: "Synthetic invited user"}
	_, err := c.ProvisionIdentity(ctx, identity)
	assertAPI(t, "required registration code", err, 403, "invite_code_required")
	identity.InviteCode = "SYNTHETIC-INVALID"
	_, err = c.ProvisionIdentity(ctx, identity)
	assertAPI(t, "invalid registration code", err, 400, "invite_code_invalid")
	var result struct {
		Codes []struct {
			ID   uint   `json:"id"`
			Code string `json:"code"`
		} `json:"codes"`
	}
	s.admin("POST", "/invite-codes/generate", token, map[string]any{"quantity": 1, "max_uses": 1, "note": "synthetic migration fixture"}, &result)
	if len(result.Codes) != 1 || result.Codes[0].ID == 0 || result.Codes[0].Code == "" {
		t.Fatal("new invite-code schema is not writable")
	}
	identity.InviteCode = result.Codes[0].Code
	created, err := c.ProvisionIdentity(ctx, identity)
	created = must(t, "invite registration", created, err)
	if !created.Created || !created.Bound || created.UserID == 0 {
		t.Fatal("valid invite did not create identity")
	}
	f.InviteID = result.Codes[0].ID
	f.InviteUserID = created.UserID
	identity.ChannelUserID = "910000011"
	_, err = c.ProvisionIdentity(ctx, identity)
	assertAPI(t, "exhausted registration code", err, 400, "invite_code_invalid")
	s.admin("PUT", "/settings", token, map[string]any{"key": "registration_config", "value": map[string]any{"registration_enabled": true, "invite_only_enabled": false}}, nil)
}

func assertAPI(t *testing.T, name string, err error, status int, code string) {
	t.Helper()
	var api *dujiao.APIError
	if !errors.As(err, &api) || api.HTTPStatus != status || api.ErrorCode != code {
		t.Fatalf("%s: expected HTTP %d/%s", name, status, code)
	}
}

func (s *stack) checkSchema(i int, f fixture) {
	query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='invite_codes';"
	if s.driver == "postgres" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='invite_codes';"
	}
	want := "0"
	if i == 2 {
		want = "1"
	}
	if s.sql(query) != want {
		s.t.Fatal("unexpected invite-code table state in real version migration")
	}
	if i == 2 && s.sql(fmt.Sprintf("SELECT COUNT(*) FROM users WHERE id=%d AND invite_code_id=%d;", f.InviteUserID, f.InviteID)) != "1" {
		s.t.Fatal("invited user database linkage missing")
	}
	users := 1 + len(f.Cases)
	if f.InviteID != 0 {
		users++
	}
	if s.sql("SELECT COUNT(*) FROM users;") != fmt.Sprint(users) || s.sql("SELECT COUNT(*) FROM orders;") != fmt.Sprint(4*len(f.Cases)) {
		s.t.Fatal("unexpected user/order row count")
	}
	if s.sql(fmt.Sprintf("SELECT manual_stock_total FROM product_skus WHERE id=%d;", f.SKU)) != fmt.Sprint(100-len(f.Cases)) {
		s.t.Fatal("manual stock changed or was migrated twice")
	}
}

// Select stable business columns directly from the real DB. This prevents a
// surviving Redis cache from hiding lost rows. Do not print this private value.
// Mutable access timestamps and newly added schema columns are intentionally
// excluded; new invite-code fields are asserted separately.
func (s *stack) dataSnapshot() string {
	queries := []string{
		"SELECT id,email,display_name,status FROM users ORDER BY id;",
		"SELECT id,user_id,provider,provider_user_id FROM user_oauth_identities ORDER BY id;",
		"SELECT id,channel_key,channel_secret,status FROM channel_clients ORDER BY id;",
		"SELECT id,product_id,sku_code,price_amount,manual_stock_total,manual_stock_locked,manual_stock_sold FROM product_skus ORDER BY id;",
		"SELECT id,order_no,user_id,status,total_amount,wallet_paid_amount,online_paid_amount FROM orders ORDER BY id;",
		"SELECT id,user_id,balance FROM wallet_accounts ORDER BY id;",
		"SELECT id,user_id,type,direction,amount,balance_before,balance_after,reference FROM wallet_transactions ORDER BY id;",
		"SELECT id,code,amount,currency,status,redeemed_user_id,wallet_txn_id FROM gift_cards ORDER BY id;",
		"SELECT id,affiliate_profile_id,order_id,commission_amount,status,withdraw_request_id FROM affiliate_commissions ORDER BY id;",
		"SELECT id,affiliate_profile_id,amount,channel,account,status FROM affiliate_withdraw_requests ORDER BY id;",
	}
	var snapshot strings.Builder
	for _, query := range queries {
		snapshot.WriteString(s.sql(query))
		snapshot.WriteByte('\n')
	}
	return snapshot.String()
}

func (s *stack) sql(query string) string {
	s.t.Helper()
	if s.driver == "postgres" {
		return s.docker("exec", s.pg, "psql", "-U", "fixture", "-d", s.dbName, "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", ".timeout 5000", filepath.Join(s.dbDir, "dujiao.db"), query)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		_ = os.WriteFile(filepath.Join(s.dir, "sqlite-error.log"), []byte(stderr.String()), 0600)
		s.t.Fatal("SQLite assertion failed; inspect private sqlite-error.log")
	}
	return strings.TrimSpace(string(out))
}

func (s *stack) checkDatabase() string {
	t := s.t
	if s.driver == "sqlite" {
		if s.sql("PRAGMA integrity_check;") != "ok" || s.sql("PRAGMA foreign_key_check;") != "" {
			t.Fatal("SQLite integrity/foreign-key check failed")
		}
		for _, idx := range []struct{ table, name string }{{"user_oauth_identities", "idx_user_oauth_identity_user_provider"}, {"wallet_accounts", "idx_wallet_accounts_user_id"}, {"wallet_transactions", "idx_wallet_transactions_reference"}, {"gift_cards", "idx_gift_cards_code"}, {"affiliate_commissions", "idx_affiliate_commission_unique"}} {
			if s.sql(fmt.Sprintf(`SELECT COUNT(*) FROM pragma_index_list('%s') WHERE name='%s' AND "unique"=1;`, idx.table, idx.name)) != "1" {
				t.Fatalf("required unique index missing: %s", idx.name)
			}
		}
	} else {
		if s.sql("SELECT COUNT(*) FROM pg_index i JOIN pg_class c ON i.indexrelid=c.oid JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='public' AND i.indisunique AND i.indisvalid AND c.relname IN ('idx_user_oauth_identity_user_provider','idx_wallet_accounts_user_id','idx_wallet_transactions_reference','idx_gift_cards_code','idx_affiliate_commission_unique');") != "5" {
			t.Fatal("required unique indexes missing or invalid")
		}
		if s.sql("SELECT COUNT(*) FROM pg_constraint WHERE contype='f' AND NOT convalidated;") != "0" {
			t.Fatal("unvalidated PostgreSQL foreign keys")
		}
	}
	markers := s.sql("SELECT key,value_json FROM settings WHERE key LIKE 'migration/%' ORDER BY key;")
	if !strings.Contains(markers, "migration/product_sku_v1") {
		t.Fatal("real upstream migration markers absent")
	}
	return markers
}

func (s *stack) backup() {
	if s.driver == "sqlite" {
		s.sql(".backup '" + filepath.Join(s.dir, "backup", "dujiao.db") + "'")
	} else {
		out, err := s.command(nil, "exec", s.pg, "pg_dump", "-U", "fixture", "-d", "fixture", "-Fc")
		if err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dir, "backup", "fixture.dump"), out, 0600); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s *stack) restore() {
	// Do not let newer queued jobs run against an old restored database. Keep
	// the upgraded queue stopped and attach a new isolated queue to the copy.
	s.docker("stop", "--time", "15", s.redis)
	s.startRedis("restored")
	if s.driver == "sqlite" {
		// Closed servers + checkpointed backup; use a new directory, never
		// overwrite the upgraded database or leave old WAL/SHM beside the copy.
		// Use a distinct bind source path; Docker Desktop may retain the old
		// mount's inode if its source directory is renamed/recreated in place.
		s.dbDir = filepath.Join(s.dir, "restored-db")
		if err := os.Mkdir(s.dbDir, 0700); err != nil {
			s.t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(s.dir, "backup", "dujiao.db"))
		if err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dbDir, "dujiao.db"), data, 0600); err != nil {
			s.t.Fatal(err)
		}
	} else {
		s.docker("exec", s.pg, "createdb", "-U", "fixture", "restored")
		data, err := os.ReadFile(filepath.Join(s.dir, "backup", "fixture.dump"))
		if err != nil {
			s.t.Fatal(err)
		}
		if _, err := s.command(data, "exec", "-i", s.pg, "pg_restore", "-U", "fixture", "-d", "restored", "--exit-on-error"); err != nil {
			s.t.Fatal(err)
		}
		path := filepath.Join(s.dir, "config.yml")
		data, err = os.ReadFile(path)
		if err != nil {
			s.t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), "dbname=fixture ", "dbname=restored ", 1))
		if err := os.WriteFile(path, data, 0600); err != nil {
			s.t.Fatal(err)
		}
		s.dbName = "restored"
	}
}
