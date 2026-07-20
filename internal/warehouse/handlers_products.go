package warehouse

import (
	"net/http"
	"strconv"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/quark"
)

func parseID(c *nucleus.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		_ = c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, false
	}
	return id, true
}

// listProducts returns the catalogue, newest first (primary client: the list
// backs read-after-write flows in the UI and the E2E suite).
func (m *module) listProducts(c *nucleus.Context) error {
	products, err := quark.For[Product](c.Request.Context(), m.bridgedPG).
		OrderBy("id", "DESC").List()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"products": products, "count": len(products)})
}

func (m *module) getProduct(c *nucleus.Context) error {
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	p, err := quark.For[Product](c.Request.Context(), m.bridgedPG).Find(id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	return c.JSON(http.StatusOK, p)
}

func (m *module) createProduct(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	var in struct {
		SKU        string `json:"sku"`
		Name       string `json:"name"`
		PriceCents int64  `json:"price_cents"`
		Stock      int64  `json:"stock"`
	}
	if err := c.BindJSON(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	if in.SKU == "" || in.Name == "" || in.PriceCents <= 0 || in.Stock < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "sku, name, positive price_cents and non-negative stock are required"})
	}

	ctx := c.Request.Context()
	p := Product{SKU: in.SKU, Name: in.Name, PriceCents: in.PriceCents, Stock: in.Stock}
	if err := quark.For[Product](ctx, m.bridgedPG).Create(&p); err != nil {
		return err
	}

	// Audit trail on the aliased MySQL database (best-effort: the audit
	// write is not transactional with the PG write, and says so).
	mv := StockMovement{ProductID: p.ID, Delta: in.Stock, Reason: "initial"}
	if err := quark.For[StockMovement](ctx, m.bridgedMy).Create(&mv); err != nil {
		m.log.Error("warehouse: audit write failed", "product_id", p.ID, "error", err)
	}

	return c.JSON(http.StatusCreated, p)
}

func (m *module) updateProduct(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	var in struct {
		Name       *string `json:"name"`
		PriceCents *int64  `json:"price_cents"`
	}
	if err := c.BindJSON(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	updates := map[string]any{}
	if in.Name != nil {
		updates["name"] = *in.Name
	}
	if in.PriceCents != nil {
		updates["price_cents"] = *in.PriceCents
	}
	if len(updates) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "nothing to update"})
	}

	ctx := c.Request.Context()
	rows, err := quark.For[Product](ctx, m.bridgedPG).Where("id", "=", id).UpdateMap(updates)
	if err != nil {
		return err
	}
	if rows == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	p, err := quark.For[Product](ctx, m.bridgedPG).Find(id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

func (m *module) deleteProduct(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	rows, err := quark.For[Product](c.Request.Context(), m.bridgedPG).
		Where("id", "=", id).DeleteBy()
	if err != nil {
		return err
	}
	if rows == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	return c.NoContent()
}

// listStockMovements reads the audit trail from the aliased MySQL database.
func (m *module) listStockMovements(c *nucleus.Context) error {
	q := quark.For[StockMovement](c.Request.Context(), m.bridgedMy).OrderBy("id", "DESC")
	if pid := c.Query("product_id"); pid != "" {
		q = q.Where("product_id", "=", pid)
	}
	movements, err := q.List()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"movements": movements, "count": len(movements)})
}

// inventoryReport reads through the replica-routed client: with a streaming
// replica configured, these SELECTs run on the standby, not the primary.
func (m *module) inventoryReport(c *nucleus.Context) error {
	ctx := c.Request.Context()
	products, err := quark.For[Product](ctx, m.deps.PGRead).OrderBy("sku", "ASC").List()
	if err != nil {
		return err
	}
	var totalStock int64
	items := make([]map[string]any, 0, len(products))
	for _, p := range products {
		totalStock += p.Stock
		items = append(items, map[string]any{"sku": p.SKU, "name": p.Name, "stock": p.Stock})
	}
	orderCount, err := quark.For[Order](ctx, m.deps.PGRead).Count()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"read_path":     m.readPath(),
		"product_count": len(products),
		"total_stock":   totalStock,
		"order_count":   orderCount,
		"items":         items,
	})
}

func (m *module) readPath() string {
	if m.deps.PG != m.deps.PGRead {
		return "replica"
	}
	return "primary"
}
