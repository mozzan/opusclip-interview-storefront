package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Product is a catalog row in canonical form.
type Product struct {
	SKU         string `json:"sku"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	PriceCents  int64  `json:"price_cents"`
	Currency    string `json:"currency"`
	Inventory   int64  `json:"inventory"`
	Status      string `json:"-"`
	UpdatedAt   string `json:"-"`
}

// Plan is a membership plan in canonical form.
type Plan struct {
	PlanID     string `json:"plan_id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Currency   string `json:"currency"`
	Interval   string `json:"interval"`
	Active     bool   `json:"-"`
}

// Rejection is one row the import refused.
type Rejection struct {
	Line   int    `json:"line"`
	SKU    string `json:"sku,omitempty"`
	PlanID string `json:"plan_id,omitempty"`
	Reason string `json:"reason"`
}

// Section is one file's part of GET /admin/import-report.
type Section struct {
	RowsRead int         `json:"rows_read"`
	Imported int         `json:"imported"`
	Rejected []Rejection `json:"rejected"`
	Warnings []string    `json:"warnings"`
}

var skuPattern = regexp.MustCompile(`^[A-Z0-9-]+$`)

// CanonicalSKU trims and upper-cases a SKU.
func CanonicalSKU(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// Validate normalizes p in place and returns the first SPEC section 2 rule it breaks.
func (p *Product) Validate() error {
	p.SKU = CanonicalSKU(p.SKU)
	p.Name = strings.TrimSpace(p.Name)
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	p.Status = strings.ToLower(strings.TrimSpace(p.Status))
	switch {
	case !skuPattern.MatchString(p.SKU):
		return fmt.Errorf("sku %q must contain only A-Z, 0-9 and -", p.SKU)
	case p.Name == "":
		return errors.New("name is required")
	case p.PriceCents <= 0:
		return fmt.Errorf("price_cents must be greater than 0, got %d", p.PriceCents)
	case p.Currency != "USD":
		return fmt.Errorf("currency must be USD, got %q", p.Currency)
	case p.Inventory < 0:
		return fmt.Errorf("inventory must be 0 or more, got %d", p.Inventory)
	case p.Status != "active" && p.Status != "archived":
		return fmt.Errorf("status must be active or archived, got %q", p.Status)
	}
	return nil
}

// csvRows reads a CSV with a header and calls fn for every data record with its starting line and a
// column getter. It returns the number of records read.
func csvRows(r io.Reader, fn func(line int, col func(string) string, fieldErr error)) (int, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return 0, err
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))] = i
	}
	n := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return n, nil
		}
		n++
		line, _ := cr.FieldPos(0)
		var fieldErr error
		if err != nil {
			fieldErr = fmt.Errorf("unreadable CSV row: %v", err)
		} else if len(rec) != len(header) {
			fieldErr = fmt.Errorf("expected %d columns, got %d", len(header), len(rec))
		}
		col := func(name string) string {
			if i, ok := idx[name]; ok && i < len(rec) {
				return rec[i]
			}
			return ""
		}
		fn(line, col, fieldErr)
	}
}

func parseWhole(field, v string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a whole number", field, v)
	}
	return n, nil
}

// parseUpdatedAt accepts ISO 8601, falling back to the supplier feed's "MM/DD/YYYY HH:MM" (assumed UTC).
func parseUpdatedAt(v string) (time.Time, bool, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), true, nil
	}
	if t, err := time.Parse("01/02/2006 15:04", v); err == nil {
		return t, false, nil
	}
	return time.Time{}, false, fmt.Errorf("updated_at %q is not a date", v)
}

type productRow struct {
	p       Product
	line    int
	updated time.Time
}

// LoadProducts parses products.csv. Every record ends up imported or rejected.
func LoadProducts(r io.Reader) ([]Product, Section, error) {
	sec := Section{Rejected: []Rejection{}, Warnings: []string{}}
	kept := map[string]*productRow{}
	var order []string
	reject := func(line int, sku, reason string) {
		sec.Rejected = append(sec.Rejected, Rejection{Line: line, SKU: sku, Reason: reason})
	}
	n, err := csvRows(r, func(line int, col func(string) string, fieldErr error) {
		sku := CanonicalSKU(col("sku"))
		if fieldErr != nil {
			reject(line, sku, fieldErr.Error())
			return
		}
		price, err := parseWhole("price_cents", col("price_cents"))
		if err != nil {
			reject(line, sku, err.Error()+" (looks like dollars, not cents; not guessing)")
			return
		}
		inv, err := parseWhole("inventory", col("inventory"))
		if err != nil {
			reject(line, sku, err.Error())
			return
		}
		p := Product{SKU: sku, Name: col("name"), Description: col("description"), Category: strings.TrimSpace(col("category")),
			PriceCents: price, Currency: col("currency"), Inventory: inv, Status: col("status")}
		if err := p.Validate(); err != nil {
			reject(line, sku, err.Error())
			return
		}
		updated, iso, err := parseUpdatedAt(col("updated_at"))
		if err != nil {
			reject(line, sku, err.Error())
			return
		}
		p.UpdatedAt = updated.Format(time.RFC3339)
		if !iso {
			sec.Warnings = append(sec.Warnings, fmt.Sprintf("line %d %s: updated_at %q is not ISO 8601; read as UTC %s", line, sku, col("updated_at"), p.UpdatedAt))
		}
		if strings.ContainsAny(p.Name+p.Description, "ÃÂ") {
			sec.Warnings = append(sec.Warnings, fmt.Sprintf("line %d %s: text looks mis-encoded (%q)", line, sku, p.Name))
		}
		if p.PriceCents >= 100000 {
			sec.Warnings = append(sec.Warnings, fmt.Sprintf("line %d %s: price $%.2f is unusually high; check for a typo", line, sku, float64(p.PriceCents)/100))
		}
		row := &productRow{p: p, line: line, updated: updated}
		prev, dup := kept[sku]
		switch {
		case !dup:
			kept[sku] = row
			order = append(order, sku)
		case updated.After(prev.updated):
			reject(prev.line, sku, fmt.Sprintf("duplicate SKU; line %d has a newer updated_at", line))
			kept[sku] = row
		default:
			reject(line, sku, fmt.Sprintf("duplicate SKU; line %d has a newer or equal updated_at", prev.line))
		}
	})
	if err != nil {
		return nil, sec, err
	}
	sec.RowsRead = n
	products := make([]Product, 0, len(order))
	for _, sku := range order {
		products = append(products, kept[sku].p)
	}
	sec.Imported = len(products)
	sort.Slice(sec.Rejected, func(i, j int) bool { return sec.Rejected[i].Line < sec.Rejected[j].Line })
	return products, sec, nil
}

// LoadPlans parses plans.csv and warns when an annual plan is not 10x its monthly tier.
func LoadPlans(r io.Reader) ([]Plan, Section, error) {
	sec := Section{Rejected: []Rejection{}, Warnings: []string{}}
	var plans []Plan
	seen := map[string]bool{}
	n, err := csvRows(r, func(line int, col func(string) string, fieldErr error) {
		id := strings.TrimSpace(col("plan_id"))
		reject := func(reason string) {
			sec.Rejected = append(sec.Rejected, Rejection{Line: line, PlanID: id, Reason: reason})
		}
		if fieldErr != nil {
			reject(fieldErr.Error())
			return
		}
		price, err := parseWhole("price_cents", col("price_cents"))
		p := Plan{PlanID: id, Name: strings.TrimSpace(col("name")), PriceCents: price,
			Currency: strings.ToUpper(strings.TrimSpace(col("currency"))), Interval: strings.ToLower(strings.TrimSpace(col("interval")))}
		active := strings.ToLower(strings.TrimSpace(col("active")))
		p.Active = active == "true"
		switch {
		case id == "":
			reject("plan_id is required")
		case seen[id]:
			reject("duplicate plan_id")
		case err != nil:
			reject(err.Error() + " (looks like dollars, not cents; not guessing)")
		case p.PriceCents <= 0:
			reject("price_cents must be greater than 0")
		case p.Name == "":
			reject("name is required")
		case p.Currency != "USD":
			reject(fmt.Sprintf("currency must be USD, got %q", p.Currency))
		case p.Interval != "month" && p.Interval != "year":
			reject(fmt.Sprintf("interval must be month or year, got %q", p.Interval))
		case active != "true" && active != "false":
			reject(fmt.Sprintf("active must be true or false, got %q", active))
		default:
			seen[id] = true
			plans = append(plans, p)
		}
	})
	if err != nil {
		return nil, sec, err
	}
	sec.RowsRead, sec.Imported = n, len(plans)
	monthly := map[string]int64{}
	for _, p := range plans {
		if p.Interval == "month" {
			monthly[tier(p.PlanID)] = p.PriceCents
		}
	}
	for _, p := range plans {
		if m, ok := monthly[tier(p.PlanID)]; ok && p.Interval == "year" && p.PriceCents != 10*m {
			sec.Warnings = append(sec.Warnings, fmt.Sprintf("%s costs %d but finance's rule says 10 x %d = %d; imported as written, please confirm",
				p.PlanID, p.PriceCents, m, 10*m))
		}
	}
	return plans, sec, nil
}

func tier(planID string) string { t, _, _ := strings.Cut(planID, "-"); return t }
