package model

type ProjectRow struct {
	Seq          int
	Section      string
	DealID       int
	DealTitle    string
	Location     string
	Investor     string
	Description  string
	Progress     string
	Support      string
	DateRange    string
	Jobs         string
	InvestPlan   string
	OwnPlan      string
	LoanPlan     string
	ProjectStage string
}

type TaskRow struct {
	DealID      int
	DealTitle   string
	ProjectID   int
	LinkSource  string
	TaskID      string
	TaskTitle   string
	Responsible string
	Deadline    string
	Status      string
	Comment     string
}
