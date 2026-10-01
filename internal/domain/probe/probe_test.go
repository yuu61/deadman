package probe

import "testing"

// Only a success is a success: every other code, the zero value and an unknown code
// included, fails closed, so the result bar draws it as a failure.
func TestResultIsSuccess(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want bool
	}{
		{"success", SuccessResult(1), true},
		{"failed", FailedResult(), false},
		{"relay_timeout", Result{Code: RelayTimeout}, false},
		{"relay_failed", Result{Code: RelayFailed}, false},
		{"unavailable", UnavailableResult(), false},
		{"zero_value", Result{}, false},
		{"unknown_code", Result{Code: ResultCode(99)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.res.IsSuccess(); got != c.want {
				t.Errorf("%+v.IsSuccess() = %v, want %v", c.res, got, c.want)
			}
		})
	}
}

// Only an observed success or target non-response enters the packet statistics.
func TestResultIsObserved(t *testing.T) {
	for _, c := range []struct {
		res  Result
		want bool
	}{
		{SuccessResult(1), true},
		{FailedResult(), true},
		{UnavailableResult(), false},
		{Result{Code: RelayTimeout}, false},
		{Result{Code: RelayFailed}, false},
		{Result{Code: 99}, false},
	} {
		if got := c.res.IsObserved(); got != c.want {
			t.Errorf("IsObserved(%+v) = %t, want %t", c.res, got, c.want)
		}
	}
}
