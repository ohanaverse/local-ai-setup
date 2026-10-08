package modeladmin

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// The names of a model's inputs, as FieldError reports them. They are the
// CLI's flag names, so an error reads the same from a flag and from the form.
const (
	FieldProvider           = "provider"
	FieldName               = "name"
	FieldID                 = "id"
	FieldFamily             = "family"
	FieldTags               = "tags"
	FieldLocation           = "location"
	FieldInputPrice         = "input-price"
	FieldCachePrice         = "cache-price"
	FieldOutputPrice        = "output-price"
	FieldSubscriptionPrice  = "subscription-price"
	FieldSubscriptionPeriod = "subscription-period"
)

// FieldError is a value the user gave that cannot be used. Field names the
// input, so the form can put the cursor on it.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Fields are a model's editable fields as the user typed them: a flag's
// value or a form input. A nil field was not given and is left alone. An
// empty one clears the key, except Family, which a model must have.
type Fields struct {
	Family             *string
	Tags               *string // comma-separated
	Location           *string // local, cloud, or "" to inherit the provider's
	InputPrice         *string // $ per million tokens
	CachePrice         *string
	OutputPrice        *string
	SubscriptionPrice  *string
	SubscriptionPeriod *string // month or year
}

// Empty reports whether no field was given.
func (f Fields) Empty() bool {
	return f.Family == nil && f.Tags == nil && f.Location == nil && f.InputPrice == nil && f.CachePrice == nil &&
		f.OutputPrice == nil && f.SubscriptionPrice == nil && f.SubscriptionPeriod == nil
}

// priceKeys maps each price field to its key in the model row.
var priceKeys = []struct{ field, key string }{
	{FieldInputPrice, "cost.input_price_per_million"},
	{FieldCachePrice, "cost.cache_price_per_million"},
	{FieldOutputPrice, "cost.output_price_per_million"},
	{FieldSubscriptionPrice, "cost.subscription_price"},
}

func (f Fields) price(field string) *string {
	switch field {
	case FieldInputPrice:
		return f.InputPrice
	case FieldCachePrice:
		return f.CachePrice
	case FieldOutputPrice:
		return f.OutputPrice
	}
	return f.SubscriptionPrice
}

// patch turns the fields into the keys to set and the keys to delete on a
// model row (config.RegistryDoc.PatchModel's arguments). It checks each field
// by itself; the one rule that needs two fields is checkSubscription's.
func (f Fields) patch() (set map[string]any, unset []string, err error) {
	set = map[string]any{}
	if f.Family != nil {
		family := strings.TrimSpace(*f.Family)
		if family == "" {
			return nil, nil, fieldErr(FieldFamily, "family is required")
		}
		set["family"] = family
	}
	if f.Tags != nil {
		tags := config.ParseFilterList(*f.Tags)
		if tags == nil {
			tags = []string{}
		}
		set["tags"] = tags
	}
	if f.Location != nil {
		switch loc := strings.TrimSpace(*f.Location); loc {
		case "":
			unset = append(unset, "location")
		case string(config.LocationLocal), string(config.LocationCloud):
			set["location"] = loc
		default:
			return nil, nil, fieldErr(FieldLocation, "location must be local or cloud, got %q", loc)
		}
	}
	for _, p := range priceKeys {
		text := f.price(p.field)
		if text == nil {
			continue
		}
		price, given, err := ParsePrice(p.field, *text)
		if err != nil {
			return nil, nil, err
		}
		if given {
			set[p.key] = price
		} else {
			unset = append(unset, p.key)
		}
	}
	if f.SubscriptionPeriod != nil {
		switch period := strings.TrimSpace(*f.SubscriptionPeriod); period {
		case "":
			unset = append(unset, "cost.subscription_period")
		case "month", "year":
			set["cost.subscription_period"] = period
		default:
			return nil, nil, fieldErr(FieldSubscriptionPeriod, "subscription period must be month or year, got %q", period)
		}
	}
	return set, unset, nil
}

// ParsePrice reads a price a user typed: empty is "no price" (given false);
// otherwise a finite number that is not negative. modelman's rule
// (screens/forms.py _parse_price), with the field named as the flag is.
func ParsePrice(field, text string) (price float64, given bool, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false, nil
	}
	price, perr := strconv.ParseFloat(text, 64)
	switch {
	case perr != nil && !math.IsInf(price, 0):
		return 0, false, fieldErr(field, "%s must be a number, got %q", field, text)
	case math.IsNaN(price) || math.IsInf(price, 0):
		return 0, false, fieldErr(field, "%s must be finite", field)
	case price < 0:
		return 0, false, fieldErr(field, "%s must not be negative", field)
	}
	return price, true, nil
}

// checkSubscription is the one rule over two fields: a subscription price
// needs a period. price and period are the row's values once the patch is
// applied.
func checkSubscription(hasPrice bool, period string) error {
	if hasPrice && period != "month" && period != "year" {
		return fieldErr(FieldSubscriptionPeriod, "a subscription price needs a subscription period (month or year)")
	}
	return nil
}

// DeriveID is the id `wt model add` gives a model when --id does not. A model
// of a local provider gets config.DiscoveredModelID: the id wt already lists,
// routes and keeps history under for that artifact, so registering a model
// that is on disk does not change its id. Any other provider gets modelman's
// rule for a cloud gateway, the provider and the name with each "/" in the
// name spelled "--", so ids agree across the two tools and existing history
// keeps matching. One case differs from modelman: its form kept the name of
// a native provider's model (auth.type "native": an agent's own provider
// row, such as claude) as it was, slashes and all. wt writes "--" there too,
// so that every derived cloud id has one "/"; --id gives any other spelling.
func DeriveID(providerID, modelName string) string {
	if localmodels.Family(providerID) != "" {
		return config.DiscoveredModelID(providerID, modelName)
	}
	return providerID + "/" + strings.ReplaceAll(modelName, "/", "--")
}
