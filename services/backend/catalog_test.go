package main

import (
	"os"
	"strings"
	"testing"
)

func TestLoadProductsFromData(t *testing.T) {
	f, err := os.Open("../../data/products.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	products, sec, err := LoadProducts(f)
	if err != nil {
		t.Fatal(err)
	}
	if sec.RowsRead != 49 || sec.Imported+len(sec.Rejected) != 49 {
		t.Fatalf("rows_read=%d imported=%d rejected=%d", sec.RowsRead, sec.Imported, len(sec.Rejected))
	}
	bySKU := map[string]Product{}
	for _, p := range products {
		bySKU[p.SKU] = p
	}
	for _, sku := range []string{"TRAIL-HEADLAMP-200", "KNIFE-FOLD-EU", "CHAIR-CAMP-FOLD", "HAMMOCK-NYLON", "BAG-STUFF-SET", "BOTTLE-KIDS-350"} {
		if _, ok := bySKU[sku]; ok {
			t.Errorf("%s should be rejected", sku)
		}
	}
	if p := bySKU["MUG-CERAMIC-01"]; p.PriceCents != 1800 || p.Inventory != 40 {
		t.Errorf("duplicate SKU should keep the newer row, got %+v", p)
	}
	if p := bySKU["CAP-TRUCKER-BLK"]; p.Status != "active" {
		t.Errorf("ACTIVE should normalize, got %+v", p)
	}
	if p := bySKU["BANDANA-PAISLEY"]; p.Currency != "USD" {
		t.Errorf("usd should normalize, got %+v", p)
	}
	if p := bySKU["GIFT-CARD-100"]; p.Inventory != 50 {
		t.Errorf("GIFT-CARD-100 inventory must come from the inventory column, got %d", p.Inventory)
	}
	if !strings.Contains(bySKU["GIFTBOX-SAMPLER"].Description, "\n") {
		t.Errorf("multiline description lost")
	}
}

func TestLoadPlansFromData(t *testing.T) {
	f, err := os.Open("../../data/plans.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	plans, sec, err := LoadPlans(f)
	if err != nil {
		t.Fatal(err)
	}
	if sec.RowsRead != 5 || sec.Imported+len(sec.Rejected) != 5 {
		t.Fatalf("bad counts %+v", sec)
	}
	if len(sec.Rejected) != 1 || sec.Rejected[0].PlanID != "team-monthly" {
		t.Errorf("expected team-monthly rejected, got %+v", sec.Rejected)
	}
	if len(sec.Warnings) != 1 || !strings.Contains(sec.Warnings[0], "pro-annual") {
		t.Errorf("expected pro-annual warning, got %v", sec.Warnings)
	}
	_ = plans
}
