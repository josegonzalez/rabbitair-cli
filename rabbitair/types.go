package rabbitair

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Enum is a named integer value reported by the device.
type Enum struct {
	names []string
}

func (e Enum) name(v int) string {
	if v >= 0 && v < len(e.names) && e.names[v] != "" {
		return e.names[v]
	}
	return fmt.Sprintf("unknown(%d)", v)
}

func (e Enum) parse(kind, s string) (int, error) {
	for i, n := range e.names {
		if n != "" && strings.EqualFold(n, s) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("invalid %s %q (valid: %s)", kind, s, strings.Join(e.Names(), ", "))
}

// Names returns the lowercase names of the valid values.
func (e Enum) Names() []string {
	var out []string
	for _, n := range e.names {
		if n != "" {
			out = append(out, strings.ToLower(n))
		}
	}
	return out
}

// Enumerations used by the protocol.
var (
	Models        = Enum{[]string{"", "MinusA2", "BioGS", "A3"}}
	Modes         = Enum{[]string{"Auto", "Pollen", "Manual"}}
	Speeds        = Enum{[]string{"SuperSilent", "Silent", "Low", "Medium", "High", "Turbo"}}
	Qualities     = Enum{[]string{"Lowest", "Low", "Medium", "High", "Highest"}}
	Sensitivities = Enum{[]string{"High", "Medium", "Low"}}
	Moodlights    = Enum{[]string{"Off", "On", "Auto", "Preset2", "Preset3"}}
	Lights        = Enum{[]string{"Off", "On", "Auto"}}
	FilterTypes   = Enum{[]string{"Unknown", "ToxinAbsorber", "OdorRemover", "GermDefense", "PetAllergy"}}
	Gases         = Enum{[]string{"Preheat", "Level1", "Level2", "Level3", "Level4"}}
	TimerModes    = Enum{[]string{"Off", "On", "Schedule"}}
	Errors        = Enum{[]string{"NoError", "DustSensor", "GasSensor", "GasAndDust", "FanLow", "NFC", "", "", "HallSensor"}}
)

// ModelBioGS is the BioGS model number, whose air quality is offset by one.
const ModelBioGS = 2

// State is the device state returned by the state command.
type State struct {
	Model             *int    `json:"model,omitempty"`
	Firmware          []int   `json:"firmware,omitempty"`
	Power             *bool   `json:"power,omitempty"`
	Mode              *int    `json:"mode,omitempty"`
	Speed             *int    `json:"speed,omitempty"`
	Quality           *int    `json:"quality,omitempty"`
	Sensitivity       *int    `json:"sensitivity,omitempty"`
	Ionizer           *bool   `json:"ionizer,omitempty"`
	Idle              *int    `json:"idle,omitempty"`
	Moodlight         *int    `json:"moodlight,omitempty"`
	Sleep             *bool   `json:"sleep,omitempty"`
	FilterCleaning    *bool   `json:"filter_cleaning,omitempty"`
	FilterReplacement *bool   `json:"filter_replacement,omitempty"`
	FilterLife        *int    `json:"filter_life,omitempty"`
	LightSensor       *bool   `json:"light_sensor,omitempty"`
	ParticulateSensor *int    `json:"particulate_sensor,omitempty"`
	FilterTimer       *int    `json:"filter_timer,omitempty"`
	AllLightOff       *int    `json:"all_light_off,omitempty"`
	Error             *int    `json:"error,omitempty"`
	TagState          *int    `json:"tag_state,omitempty"`
	TagUID            []int   `json:"tag_uid,omitempty"`
	FilterType        *int    `json:"filter_type,omitempty"`
	PMSensor          []int   `json:"pm_sensor,omitempty"`
	Color             []int   `json:"color,omitempty"`
	LightSensorCtl    *bool   `json:"lsens_ctl,omitempty"`
	FilterCtl         *bool   `json:"filter_ctl,omitempty"`
	Buzzer            *bool   `json:"buzzer,omitempty"`
	Gas               *int    `json:"gas,omitempty"`
	Lock              *bool   `json:"lock,omitempty"`
	Open              *bool   `json:"open,omitempty"`
	TimerMode         *int    `json:"timer_mode,omitempty"`
	Timer             *int    `json:"timer,omitempty"`
	Schedule          *string `json:"schedule,omitempty"`
	RSSI              *int    `json:"rssi,omitempty"`
	WifiFirmware      *string `json:"v,omitempty"`

	// Raw is the undecoded response data.
	Raw json.RawMessage `json:"-"`
}

// ModelName returns the model name, or "" if unknown.
func (s *State) ModelName() string {
	if s.Model == nil {
		return ""
	}
	return Models.name(*s.Model)
}

// QualityName returns the air quality as a name, accounting for BioGS.
func (s *State) QualityName() string {
	if s.Quality == nil {
		return ""
	}
	q := *s.Quality
	if s.Model != nil && *s.Model == ModelBioGS {
		q--
	}
	return Qualities.name(q)
}

// RSSI holds Wi-Fi signal statistics.
type RSSI struct {
	Cur *int `json:"cur,omitempty"`
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
	Avg *int `json:"avg,omitempty"`
}

// Info is the device information returned by the info command.
type Info struct {
	Name           string  `json:"name"`
	MCU            string  `json:"mcu"`
	Build          string  `json:"build"`
	MAC            string  `json:"mac"`
	Time           *string `json:"time,omitempty"`
	Uptime         int64   `json:"uptime"`
	MotorUptime    int64   `json:"mup"`
	WifiUptime     int64   `json:"wup"`
	InternetUptime *int64  `json:"iup,omitempty"`
	CloudUptime    *int64  `json:"cup,omitempty"`
	MainFirmware   *string `json:"fv,omitempty"`
	RSSI           *RSSI   `json:"rssi,omitempty"`

	// Raw is the undecoded response data.
	Raw json.RawMessage `json:"-"`
}

// Update is a set of settings to change. Nil fields are left alone.
type Update struct {
	Power             *bool   `json:"power,omitempty"`
	Mode              *int    `json:"mode,omitempty"`
	Speed             *int    `json:"speed,omitempty"`
	Sensitivity       *int    `json:"sensitivity,omitempty"`
	Ionizer           *bool   `json:"ionizer,omitempty"`
	Moodlight         *int    `json:"moodlight,omitempty"`
	FilterCleaning    *bool   `json:"filter_cleaning,omitempty"`
	FilterReplacement *bool   `json:"filter_replacement,omitempty"`
	FilterLife        *int    `json:"filter_life,omitempty"`
	FilterTimer       *int    `json:"filter_timer,omitempty"`
	Lights            *int    `json:"all_light_off,omitempty"`
	Color             []int   `json:"color,omitempty"`
	LightSensorCtl    *bool   `json:"lsens_ctl,omitempty"`
	FilterCtl         *bool   `json:"filter_ctl,omitempty"`
	Buzzer            *bool   `json:"buzzer,omitempty"`
	ChildLock         *bool   `json:"lock,omitempty"`
	TimerMode         *int    `json:"timer_mode,omitempty"`
	Timer             *int    `json:"timer,omitempty"`
	Schedule          *string `json:"schedule,omitempty"`
}

// Empty reports whether the update changes nothing.
func (u *Update) Empty() bool {
	b, _ := json.Marshal(u)
	return string(b) == "{}"
}

// Validate checks value ranges, matching python-rabbitair.
func (u *Update) Validate() error {
	if u.Empty() {
		return errors.New("nothing to change")
	}
	if u.Speed != nil && *u.Speed == 0 {
		return errors.New("supersilent speed cannot be set manually")
	}
	for name, v := range map[string]*int{"filter life": u.FilterLife, "filter timer": u.FilterTimer} {
		if v != nil && (*v < 0 || *v > 525600) {
			return fmt.Errorf("%s must be in the range 0-525600", name)
		}
	}
	if u.Timer != nil && (*u.Timer < 0 || *u.Timer > 1440) {
		return errors.New("timer must be in the range 0-1440")
	}
	if u.Color != nil {
		if len(u.Color) != 9 {
			return errors.New("color must have 9 values")
		}
		if slices.ContainsFunc(u.Color, func(v int) bool { return v < 0 || v > 40 }) {
			return errors.New("color values must be in the range 0-40")
		}
	}
	if u.Schedule != nil {
		s := *u.Schedule
		if len(s) != 24 {
			return errors.New("schedule must be 24 characters")
		}
		for _, ch := range s {
			if (ch < '0' || ch > '5') && ch != 'A' {
				return errors.New("schedule characters must be 0-5 or A")
			}
		}
	}
	return nil
}

// ParseEnum converts a name to its value for the given enumeration.
func ParseEnum(e Enum, kind, s string) (int, error) { return e.parse(kind, s) }

// EnumName converts a value to its name for the given enumeration.
func EnumName(e Enum, v int) string { return e.name(v) }
