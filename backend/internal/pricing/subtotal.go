package pricing

// Subtotal validates and prices a draft without selecting fulfillment. It does
// not calculate a fee, payment method or final order total. Like Quote, it uses
// the trusted engine snapshot and retains already selected unavailable items.
func (e *Engine) Subtotal(draft []Unit) (int64, error) {
	return e.validate(draft)
}
