package webhook

type Payload struct {
	Event       string      `json:"event"`
	DeliveryID  string      `json:"delivery_id"`
	Timestamp   string      `json:"timestamp"`
	Project     Project     `json:"project"`
	Request     Request     `json:"request"`
	Submitter   Person      `json:"submitter"`
	Attribution Attribution `json:"attribution"`
	Actor       Actor       `json:"actor"`
	Comment     *Comment    `json:"comment,omitempty"`
	Change      *Change     `json:"change,omitempty"`
}

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Request struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

type Person struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Attribution struct {
	Source     string `json:"source"`
	AppName    string `json:"app_name,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
}

type Actor struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	AppName string `json:"app_name,omitempty"`
}

type Comment struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

type Change struct {
	From string `json:"from"`
	To   string `json:"to"`
}
