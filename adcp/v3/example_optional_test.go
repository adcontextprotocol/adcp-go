package adcp_test

import (
	"encoding/json"
	"fmt"

	adcp "github.com/adcontextprotocol/adcp-go/adcp/v3"
)

// updateRequest stands for a request type with a field that has three states.
// Tag every adcp.Optional field `omitzero`, and never `omitempty`.
type updateRequest struct {
	DailyBudgetCap adcp.Optional[float64] `json:"daily_budget_cap,omitzero"`
}

// A buyer leaves a field out, sends null to clear it, or sends a value. Write
// Some(250.0) for a number field: Some(250) infers Optional[int].
func ExampleOptional_buyer() {
	for _, req := range []updateRequest{
		{},
		{DailyBudgetCap: adcp.Null[float64]()},
		{DailyBudgetCap: adcp.Some(250.0)},
		{DailyBudgetCap: adcp.Some(0.0)},
	} {
		wire, err := json.Marshal(req)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Println(string(wire))
	}
	// Output:
	// {}
	// {"daily_budget_cap":null}
	// {"daily_budget_cap":250}
	// {"daily_budget_cap":0}
}

// A seller reads the three states: omitted preserves what is stored, null
// clears it, a value replaces it.
func ExampleOptional_seller() {
	for _, wire := range []string{`{}`, `{"daily_budget_cap":null}`, `{"daily_budget_cap":250}`, `{"daily_budget_cap":0}`} {
		var req updateRequest
		if err := json.Unmarshal([]byte(wire), &req); err != nil {
			fmt.Println("error:", err)
			return
		}
		if req.DailyBudgetCap.IsNull() {
			fmt.Println("clear the cap")
		} else if v, ok := req.DailyBudgetCap.Get(); ok {
			fmt.Println("set the cap to", v)
		} else {
			fmt.Println("leave the cap as it is")
		}
	}
	// Output:
	// leave the cap as it is
	// clear the cap
	// set the cap to 250
	// set the cap to 0
}

// Some of a nil slice, map, pointer or interface would encode as null, which
// means clear, so encoding it is an error.
func ExampleSome() {
	_, err := json.Marshal(struct {
		Seller adcp.Optional[[]string] `json:"approved_sellers,omitzero"`
	}{adcp.Some[[]string](nil)})
	fmt.Println(err != nil)

	wire, _ := json.Marshal(struct {
		Seller adcp.Optional[[]string] `json:"approved_sellers,omitzero"`
	}{adcp.Some([]string{})})
	fmt.Println(string(wire))
	// Output:
	// true
	// {"approved_sellers":[]}
}

// Null sends an explicit null, which asks the seller to clear the field.
func ExampleNull() {
	wire, _ := json.Marshal(updateRequest{DailyBudgetCap: adcp.Null[float64]()})
	fmt.Println(string(wire))
	// Output:
	// {"daily_budget_cap":null}
}
