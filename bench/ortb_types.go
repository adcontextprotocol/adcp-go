package bench

// OpenRTB 2.6 types — realistic subset for benchmarking.
// Represents what a real SSP sends in a bid request.

type BidRequest struct {
	ID     string   `json:"id"`
	Imp    []Imp    `json:"imp"`
	Site   *Site    `json:"site,omitempty"`
	App    *App     `json:"app,omitempty"`
	Device *Device  `json:"device,omitempty"`
	User   *User    `json:"user,omitempty"`
	AT     int      `json:"at"`
	TMax   int      `json:"tmax"`
	WSeat  []string `json:"wseat,omitempty"`
	BSeat  []string `json:"bseat,omitempty"`
	Cur    []string `json:"cur,omitempty"`
	BCat   []string `json:"bcat,omitempty"`
	BAdv   []string `json:"badv,omitempty"`
	Regs   *Regs    `json:"regs,omitempty"`
	Source *Source  `json:"source,omitempty"`
	Ext    any      `json:"ext,omitempty"`
}

type Imp struct {
	ID          string  `json:"id"`
	Banner      *Banner `json:"banner,omitempty"`
	Video       *Video  `json:"video,omitempty"`
	BidFloor    float64 `json:"bidfloor"`
	BidFloorCur string  `json:"bidfloorcur"`
	Secure      int     `json:"secure"`
	PMP         *PMP    `json:"pmp,omitempty"`
	Ext         any     `json:"ext,omitempty"`
}

type Banner struct {
	W      int      `json:"w"`
	H      int      `json:"h"`
	Pos    int      `json:"pos"`
	Format []Format `json:"format,omitempty"`
	API    []int    `json:"api,omitempty"`
}

type Format struct {
	W int `json:"w"`
	H int `json:"h"`
}

type Video struct {
	MIMEs      []string `json:"mimes"`
	Protocols  []int    `json:"protocols"`
	W          int      `json:"w"`
	H          int      `json:"h"`
	StartDelay int     `json:"startdelay"`
	Linearity  int      `json:"linearity"`
	Skip       int      `json:"skip"`
	API        []int    `json:"api,omitempty"`
	MinDur     int      `json:"minduration"`
	MaxDur     int      `json:"maxduration"`
}

type Site struct {
	ID        string     `json:"id"`
	Domain    string     `json:"domain"`
	Page      string     `json:"page"`
	Cat       []string   `json:"cat,omitempty"`
	SectionCat []string  `json:"sectioncat,omitempty"`
	Ref       string     `json:"ref,omitempty"`
	Publisher *Publisher `json:"publisher,omitempty"`
	Ext       any        `json:"ext,omitempty"`
}

type App struct {
	ID     string     `json:"id"`
	Bundle string     `json:"bundle"`
	Name   string     `json:"name"`
	Cat    []string   `json:"cat,omitempty"`
	Publisher *Publisher `json:"publisher,omitempty"`
}

type Publisher struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cat  []string `json:"cat,omitempty"`
}

type Device struct {
	UA             string `json:"ua"`
	IP             string `json:"ip"`
	Geo            *Geo   `json:"geo,omitempty"`
	DeviceType     int    `json:"devicetype"`
	Make           string `json:"make"`
	Model          string `json:"model"`
	OS             string `json:"os"`
	OSV            string `json:"osv"`
	ConnectionType int    `json:"connectiontype"`
	IFA            string `json:"ifa"`
	DIDSHA1        string `json:"didsha1,omitempty"`
	DPIDSHA1       string `json:"dpidsha1,omitempty"`
	JS             int    `json:"js"`
	Language       string `json:"language"`
	W              int    `json:"w"`
	H              int    `json:"h"`
	PPI            int    `json:"ppi"`
	Ext            any    `json:"ext,omitempty"`
}

type Geo struct {
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Country string  `json:"country"`
	Region  string  `json:"region"`
	Metro   string  `json:"metro"`
	City    string  `json:"city"`
	Zip     string  `json:"zip"`
	Type    int     `json:"type"`
}

type User struct {
	ID       string `json:"id"`
	BuyerUID string `json:"buyeruid,omitempty"`
	YOB      int    `json:"yob,omitempty"`
	Gender   string `json:"gender,omitempty"`
	Geo      *Geo   `json:"geo,omitempty"`
	Ext      *UserExt `json:"ext,omitempty"`
}

type UserExt struct {
	Consent string `json:"consent,omitempty"`
	EIDs    []EID  `json:"eids,omitempty"`
}

type EID struct {
	Source string  `json:"source"`
	UIDs   []EUID  `json:"uids"`
}

type EUID struct {
	ID    string `json:"id"`
	AType int    `json:"atype"`
}

type Regs struct {
	COPPA int     `json:"coppa"`
	Ext   *RegsExt `json:"ext,omitempty"`
}

type RegsExt struct {
	GDPR      int    `json:"gdpr"`
	USPrivacy string `json:"us_privacy"`
	GPP       string `json:"gpp,omitempty"`
	GPPSID    []int  `json:"gpp_sid,omitempty"`
}

type Source struct {
	FD    int    `json:"fd"`
	TID   string `json:"tid"`
	PChain string `json:"pchain,omitempty"`
}

type PMP struct {
	Private int    `json:"private_auction"`
	Deals   []Deal `json:"deals,omitempty"`
}

type Deal struct {
	ID          string   `json:"id"`
	BidFloor    float64  `json:"bidfloor"`
	BidFloorCur string   `json:"bidfloorcur"`
	AT          int      `json:"at"`
	WSeat       []string `json:"wseat,omitempty"`
}

// BidResponse is the OpenRTB response.
type BidResponse struct {
	ID      string    `json:"id"`
	SeatBid []SeatBid `json:"seatbid"`
	Cur     string    `json:"cur"`
}

type SeatBid struct {
	Bid  []Bid  `json:"bid"`
	Seat string `json:"seat"`
}

type Bid struct {
	ID    string  `json:"id"`
	ImpID string  `json:"impid"`
	Price float64 `json:"price"`
	AdID  string  `json:"adid"`
	NURL  string  `json:"nurl,omitempty"`
	ADM   string  `json:"adm,omitempty"`
	ADomain []string `json:"adomain,omitempty"`
	CID   string  `json:"cid"`
	CrID  string  `json:"crid"`
	Cat   []string `json:"cat,omitempty"`
	W     int     `json:"w"`
	H     int     `json:"h"`
	DealID string `json:"dealid,omitempty"`
}
