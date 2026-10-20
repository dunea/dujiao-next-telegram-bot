package telegram

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func TestCatalogGroupsChoicesWithoutChangingIdentityOrPrice(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, nil)
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		f.categories = []dujiao.Category{{ID: 7, Name: "分类 A", ProductCount: 3}, {ID: 8, Name: "分类 B"}, {ID: 9, Name: strings.Repeat("长分类", 10)}}
		a.showCategories(context.Background(), 42, &models.User{ID: 42}, true, 10)
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 2 || len(k.InlineKeyboard[1]) != 1 || k.InlineKeyboard[1][0].Text != f.categories[2].Name || findCallback(k, "cat:7:1") != "cat:7:1" || findCallback(k, "cat:0:1") != "cat:0:1" || findCallback(k, "home") != "home" {
			t.Fatal("categories lost grouping, full names or original actions")
		}
		for i := range 5 {
			f.products.Items = append(f.products.Items, dujiao.Product{ID: uint(i + 3), Title: fmt.Sprintf("商品%d<&>完整名称", i+1), PriceFrom: "10.00", MemberPriceFrom: "9.00", Currency: "CNY"})
		}
		f.products.TotalPage = 3
		f.products.Items[4].StockStatus = "out_of_stock"
		a.showProducts(context.Background(), 42, 42, 7, 2, true, 10)
		k = formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		text := f.texts[len(f.texts)-1]
		if len(k.InlineKeyboard) != 5 || len(k.InlineKeyboard[0]) != 2 || !strings.Contains(text, "2 / 3") || !strings.Contains(text, "9.00 CNY") || strings.Contains(text, "10.00 CNY") || !strings.Contains(text, "商品1&lt;&amp;&gt;完整名称") {
			t.Fatal("numbered compact product list lost member price or escaped title")
		}
		for i := range 5 {
			b := k.InlineKeyboard[i/2][i%2]
			if b.CallbackData != fmt.Sprintf("product:%d", i+3) || !strings.HasPrefix(b.Text, fmt.Sprintf("%d · ", i+1)) {
				t.Fatal("product button no longer maps to its numbered entry")
			}
		}
		for _, cb := range []string{"cat:7:1", "cat:7:3", "cats", "home"} {
			if findCallback(k, cb) != cb {
				t.Fatal("catalog pagination/navigation missing", cb)
			}
		}
		query, _ := url.Parse(f.catalogQueries[len(f.catalogQueries)-1])
		if q := query.Query(); q.Get("category_id") != "7" || q.Get("page") != "2" || q.Get("channel_user_id") != "42" || q.Get("locale") != locale {
			t.Fatal("catalog query identity, locale or paging changed")
		}
		f.products.Items = nil
		f.products.TotalPage = 0
		a.showProducts(context.Background(), 42, 42, 7, 1, true, 10)
		k = formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 1 || len(k.InlineKeyboard[0]) != 2 || len(f.orders) != 0 || len(f.previews) != 0 || len(f.rechargeRequests) != 0 {
			t.Fatal("empty catalog lost direct navigation or browsing wrote an order")
		}
	}
}

func TestSKUChoicesKeepLongNamesAndUnavailableOptionsSafe(t *testing.T) {
	a, f := quantityFixture(t)
	f.product.SKUs = []dujiao.SKU{{ID: 31, SpecValues: "A", Price: "10.00", MemberPrice: "9.00"}, {ID: 32, SpecValues: "B", Price: "20.00"}, {ID: 33, SpecValues: strings.Repeat("长规格", 8), Price: "30.00", StockStatus: "out_of_stock"}}
	a.showProduct(context.Background(), 42, 42, 3, true, 10)
	k := formKeyboard(t, f)
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 2 || !strings.Contains(k.InlineKeyboard[0][0].Text, "9.00") || len(k.InlineKeyboard[1]) != 1 || !strings.Contains(k.InlineKeyboard[1][0].Text, f.product.SKUs[2].SpecValues) || k.InlineKeyboard[1][0].CallbackData != "noop" {
		t.Fatal("SKU grid changed price, clipped a long label or enabled exhausted stock")
	}
	formCallback(a, 42, 10, "qty:3:32:1")
	q := a.sessions.Get(42).Quantity
	if q == nil || q.SKUID != 32 || len(f.orders) != 0 || len(f.previews) != 0 {
		t.Fatal("SKU selection changed identity or created an order")
	}
}

func TestQuantityPrimaryAndFixedQuantityControls(t *testing.T) {
	_, f := quantityFixture(t)
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		_, k := quantityCard(locale, f.product, 31, 2500, "input-id")
		if len(k.InlineKeyboard) != 4 || len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].CallbackData != "buy:3:31:2500" || !strings.Contains(k.InlineKeyboard[0][0].Text, "2500") || len(k.InlineKeyboard[1]) != 3 || k.InlineKeyboard[1][1].CallbackData != "qtyinput:input-id" || len(k.InlineKeyboard[2]) != 3 || len(k.InlineKeyboard[3]) != 2 {
			t.Fatal("quantity primary, compact numeric controls or footer changed")
		}
	}
	f.product.MinPurchaseQuantity, f.product.MaxPurchaseQuantity = 5, 5
	_, k := quantityCard("zh-CN", f.product, 31, 5, "input-id")
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard) != 2 || findCallback(k, "buy:3:31:5") == "" || findCallback(k, "qtyinput:") != "" || findCallback(k, "qty:") != "" {
		t.Fatal("fixed-quantity purchase shows unnecessary quantity controls")
	}
	f.product.StockStatus = "out_of_stock"
	_, k = quantityCard("zh-CN", f.product, 31, 5, "input-id")
	if findCallback(k, "buy:") != "" || findCallback(k, "qtyinput:") != "" || findCallback(k, "noop") != "noop" {
		t.Fatal("sold out product became purchasable")
	}
}

func TestCheckoutCompactToolsKeepDraftRevisionsAndExplicitConfirmation(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, []dujiao.ManualFormField{{Key: "email", Type: "email", Required: true}})
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		formCallback(a, 42, 10, "buy:3:31:1")
		formReply(a, "buyer@example.invalid")
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 4 || len(k.InlineKeyboard[0]) != 1 || len(k.InlineKeyboard[1]) != 2 {
			t.Fatal("checkout primary and edit tools are not grouped")
		}
		couponAction(a, "edit")
		formReply(a, "SAVE10")
		d := a.sessions.Get(42).Purchase
		k = formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 4 || len(k.InlineKeyboard[2]) != 2 || len(f.orders) != 0 {
			t.Fatal("coupon/manual tools use unnecessary rows or created an order")
		}
		for _, cb := range []string{fmt.Sprintf("formconfirm:%s:%d", d.ID, d.Revision), fmt.Sprintf("qtyedit:%s:%d", d.ID, d.Revision), fmt.Sprintf("formedit:%s:%d", d.ID, d.Revision), fmt.Sprintf("coupon:%s:%d:remove", d.ID, d.Revision), "product:3", "home"} {
			if findCallback(k, cb) != cb {
				t.Fatal("checkout lost revision-bound action", cb)
			}
		}
		checkoutConfirm(a)
		if len(f.orders) != 1 || f.orders[0].CouponCode != "SAVE10" {
			t.Fatal("explicit compact confirmation lost coupon or created duplicates")
		}
	}
}

func TestCheckoutErrorKeepsRetryPrimaryAndNeverOffersConfirm(t *testing.T) {
	a, f := newFormFixture(t, nil)
	formCallback(a, 42, 10, "buy:3:31:1")
	couponAction(a, "edit")
	formReply(a, "BAD")
	k := formKeyboard(t, f)
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 1 || !strings.HasSuffix(k.InlineKeyboard[0][0].CallbackData, ":retry") || findCallback(k, "formconfirm:") != "" || findCallback(k, "qtyedit:") != "" || len(f.orders) != 0 {
		t.Fatal("error page lost retry hierarchy or enabled invalid checkout")
	}
}
