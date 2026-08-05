package adcp

var _ SyncGovernanceResponse = SyncGovernanceSuccess{}
var _ SyncGovernanceResponse = &SyncGovernanceSuccess{}
var _ SyncGovernanceResponse = SyncGovernanceError{}
var _ SyncGovernanceResponse = &SyncGovernanceError{}

var _ CreateMediaBuyResponse = CreateMediaBuySuccess{}
var _ CreateMediaBuyResponse = &CreateMediaBuySuccess{}
var _ CreateMediaBuyResponse = CreateMediaBuyError{}
var _ CreateMediaBuyResponse = &CreateMediaBuyError{}
var _ CreateMediaBuyResponse = CreateMediaBuySubmitted{}
var _ CreateMediaBuyResponse = &CreateMediaBuySubmitted{}

var _ ProvidePerformanceFeedbackResponse = ProvidePerformanceFeedbackSuccess{}
var _ ProvidePerformanceFeedbackResponse = &ProvidePerformanceFeedbackSuccess{}
var _ ProvidePerformanceFeedbackResponse = ProvidePerformanceFeedbackError{}
var _ ProvidePerformanceFeedbackResponse = &ProvidePerformanceFeedbackError{}

var _ ComplyTestControllerResponse = ListScenariosSuccess{}
var _ ComplyTestControllerResponse = &ListScenariosSuccess{}
var _ ComplyTestControllerResponse = StateTransitionSuccess{}
var _ ComplyTestControllerResponse = &StateTransitionSuccess{}
var _ ComplyTestControllerResponse = SimulationSuccess{}
var _ ComplyTestControllerResponse = &SimulationSuccess{}
var _ ComplyTestControllerResponse = ForcedDirectiveSuccess{}
var _ ComplyTestControllerResponse = &ForcedDirectiveSuccess{}
var _ ComplyTestControllerResponse = SeedSuccess{}
var _ ComplyTestControllerResponse = &SeedSuccess{}
var _ ComplyTestControllerResponse = ProvenanceAuditObservationsSuccess{}
var _ ComplyTestControllerResponse = &ProvenanceAuditObservationsSuccess{}
var _ ComplyTestControllerResponse = UpstreamTrafficSuccess{}
var _ ComplyTestControllerResponse = &UpstreamTrafficSuccess{}
var _ ComplyTestControllerResponse = ControllerError{}
var _ ComplyTestControllerResponse = &ControllerError{}
