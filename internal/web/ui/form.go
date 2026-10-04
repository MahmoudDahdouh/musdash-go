package ui

// Form carries submitted values and per-field errors back to a page, so a
// rejected form re-renders with what the person typed.
type Form struct {
	Values map[string]string
	Errors map[string]string
}

// V returns the submitted value of a field.
func (f Form) V(key string) string { return f.Values[key] }

// E returns the error message of a field, or "".
func (f Form) E(key string) string { return f.Errors[key] }

// Set records a value.
func (f *Form) Set(key, value string) {
	if f.Values == nil {
		f.Values = make(map[string]string)
	}
	f.Values[key] = value
}

// Fail records an error against a field.
func (f *Form) Fail(key, message string) {
	if f.Errors == nil {
		f.Errors = make(map[string]string)
	}
	f.Errors[key] = message
}

// OK reports whether no field has an error.
func (f Form) OK() bool { return len(f.Errors) == 0 }
