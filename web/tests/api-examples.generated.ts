// Generated callable wrappers; request values come from the caller/form.
import type * as API from '@backplane-labs/backplane-api';
export function AuditService_SearchAudit(client: API.AuditServiceClient, request: API.SearchAuditRequest, signal: AbortSignal) {
  return client.searchAudit(request, { signal });
}
export function AuditService_AuditHistogram(client: API.AuditServiceClient, request: API.AuditHistogramRequest, signal: AbortSignal) {
  return client.auditHistogram(request, { signal });
}
export function AuditService_AuditFields(client: API.AuditServiceClient, request: API.AuditFieldsRequest, signal: AbortSignal) {
  return client.auditFields(request, { signal });
}
export function AuditService_AuditFacets(client: API.AuditServiceClient, request: API.AuditFacetsRequest, signal: AbortSignal) {
  return client.auditFacets(request, { signal });
}
export function BindingService_ListBindings(client: API.BindingServiceClient, request: API.ListBindingsRequest, signal: AbortSignal) {
  return client.listBindings(request, { signal });
}
export function BindingService_GetBinding(client: API.BindingServiceClient, request: API.GetBindingRequest, signal: AbortSignal) {
  return client.getBinding(request, { signal });
}
export function BindingService_ListBindingVersions(client: API.BindingServiceClient, request: API.ListBindingVersionsRequest, signal: AbortSignal) {
  return client.listBindingVersions(request, { signal });
}
export function BindingService_ValidateBinding(client: API.BindingServiceClient, request: API.ValidateBindingRequest, signal: AbortSignal) {
  return client.validateBinding(request, { signal });
}
export function BindingService_SaveBinding(client: API.BindingServiceClient, request: API.SaveBindingRequest, signal: AbortSignal) {
  return client.saveBinding(request, { signal });
}
export function BindingService_RollbackBinding(client: API.BindingServiceClient, request: API.RollbackBindingRequest, signal: AbortSignal) {
  return client.rollbackBinding(request, { signal });
}
export function BindingService_DeleteBinding(client: API.BindingServiceClient, request: API.DeleteBindingRequest, signal: AbortSignal) {
  return client.deleteBinding(request, { signal });
}
export function BindingService_WatchBindings(client: API.BindingServiceClient, request: API.WatchBindingsRequest, signal: AbortSignal) {
  return client.watchBindings(request, { signal });
}
export function BindingService_TestBinding(client: API.BindingServiceClient, request: API.TestBindingRequest, signal: AbortSignal) {
  return client.testBinding(request, { signal });
}
export function BindingService_ListBindingRuns(client: API.BindingServiceClient, request: API.ListBindingRunsRequest, signal: AbortSignal) {
  return client.listBindingRuns(request, { signal });
}
export function BindingService_GetBindingRun(client: API.BindingServiceClient, request: API.GetBindingRunRequest, signal: AbortSignal) {
  return client.getBindingRun(request, { signal });
}
export function BindingService_CancelBindingRun(client: API.BindingServiceClient, request: API.CancelBindingRunRequest, signal: AbortSignal) {
  return client.cancelBindingRun(request, { signal });
}
export function CallService_CallHook(client: API.CallServiceClient, request: API.CallHookRequest, signal: AbortSignal) {
  return client.callHook(request, { signal });
}
export function CallService_RunActivity(client: API.CallServiceClient, request: API.RunActivityRequest, signal: AbortSignal) {
  return client.runActivity(request, { signal });
}
export function CatalogService_ListServices(client: API.CatalogServiceClient, request: API.ListServicesRequest, signal: AbortSignal) {
  return client.listServices(request, { signal });
}
export function CatalogService_GetService(client: API.CatalogServiceClient, request: API.GetServiceRequest, signal: AbortSignal) {
  return client.getService(request, { signal });
}
export function CatalogService_WatchCatalog(client: API.CatalogServiceClient, request: API.WatchCatalogRequest, signal: AbortSignal) {
  return client.watchCatalog(request, { signal });
}
export function CatalogService_ListPlugins(client: API.CatalogServiceClient, request: API.ListPluginsRequest, signal: AbortSignal) {
  return client.listPlugins(request, { signal });
}
export function ConfigService_GetConfig(client: API.ConfigServiceClient, request: API.GetConfigRequest, signal: AbortSignal) {
  return client.getConfig(request, { signal });
}
export function ConfigService_ListRevisions(client: API.ConfigServiceClient, request: API.ListRevisionsRequest, signal: AbortSignal) {
  return client.listRevisions(request, { signal });
}
export function ConfigService_ValidateOverride(client: API.ConfigServiceClient, request: API.ValidateOverrideRequest, signal: AbortSignal) {
  return client.validateOverride(request, { signal });
}
export function ConfigService_SaveRevision(client: API.ConfigServiceClient, request: API.SaveRevisionRequest, signal: AbortSignal) {
  return client.saveRevision(request, { signal });
}
export function ConfigService_Rollback(client: API.ConfigServiceClient, request: API.RollbackRequest, signal: AbortSignal) {
  return client.rollback(request, { signal });
}
export function ConfigService_WatchConfig(client: API.ConfigServiceClient, request: API.WatchConfigRequest, signal: AbortSignal) {
  return client.watchConfig(request, { signal });
}
export function ErrorService_SearchErrors(client: API.ErrorServiceClient, request: API.SearchErrorsRequest, signal: AbortSignal) {
  return client.searchErrors(request, { signal });
}
export function ErrorService_ErrorHistogram(client: API.ErrorServiceClient, request: API.ErrorHistogramRequest, signal: AbortSignal) {
  return client.errorHistogram(request, { signal });
}
export function ErrorService_ErrorFacets(client: API.ErrorServiceClient, request: API.ErrorFacetsRequest, signal: AbortSignal) {
  return client.errorFacets(request, { signal });
}
export function ErrorService_GetError(client: API.ErrorServiceClient, request: API.GetErrorRequest, signal: AbortSignal) {
  return client.getError(request, { signal });
}
export function ErrorService_RelatedLogs(client: API.ErrorServiceClient, request: API.RelatedLogsRequest, signal: AbortSignal) {
  return client.relatedLogs(request, { signal });
}
export function EventService_ListEvents(client: API.EventServiceClient, request: API.ListEventsRequest, signal: AbortSignal) {
  return client.listEvents(request, { signal });
}
export function EventService_GetStream(client: API.EventServiceClient, request: API.GetStreamRequest, signal: AbortSignal) {
  return client.getStream(request, { signal });
}
export function EventService_PeekMessages(client: API.EventServiceClient, request: API.PeekMessagesRequest, signal: AbortSignal) {
  return client.peekMessages(request, { signal });
}
export function EventService_PublishTestEvent(client: API.EventServiceClient, request: API.PublishTestEventRequest, signal: AbortSignal) {
  return client.publishTestEvent(request, { signal });
}
export function EventService_ListDeadLetters(client: API.EventServiceClient, request: API.ListDeadLettersRequest, signal: AbortSignal) {
  return client.listDeadLetters(request, { signal });
}
export function EventService_RedriveDeadLetters(client: API.EventServiceClient, request: API.RedriveDeadLettersRequest, signal: AbortSignal) {
  return client.redriveDeadLetters(request, { signal });
}
export function EventService_PurgeDeadLetters(client: API.EventServiceClient, request: API.PurgeDeadLettersRequest, signal: AbortSignal) {
  return client.purgeDeadLetters(request, { signal });
}
export function ObsService_GetObsCapabilities(client: API.ObsServiceClient, request: API.GetObsCapabilitiesRequest, signal: AbortSignal) {
  return client.getObsCapabilities(request, { signal });
}
export function ObsService_QueryObs(client: API.ObsServiceClient, request: API.QueryObsRequest, signal: AbortSignal) {
  return client.queryObs(request, { signal });
}
export function ObsService_GetTrace(client: API.ObsServiceClient, request: API.GetTraceRequest, signal: AbortSignal) {
  return client.getTrace(request, { signal });
}
export function ObsService_ListObsSources(client: API.ObsServiceClient, request: API.ListObsSourcesRequest, signal: AbortSignal) {
  return client.listObsSources(request, { signal });
}
export function ObsService_ListObsFields(client: API.ObsServiceClient, request: API.ListObsFieldsRequest, signal: AbortSignal) {
  return client.listObsFields(request, { signal });
}
export function ObsService_ListObsFieldValues(client: API.ObsServiceClient, request: API.ListObsFieldValuesRequest, signal: AbortSignal) {
  return client.listObsFieldValues(request, { signal });
}
export function ObsService_GetObsSelectors(client: API.ObsServiceClient, request: API.GetObsSelectorsRequest, signal: AbortSignal) {
  return client.getObsSelectors(request, { signal });
}
export function PlatformService_GetCapabilities(client: API.PlatformServiceClient, request: API.GetCapabilitiesRequest, signal: AbortSignal) {
  return client.getCapabilities(request, { signal });
}
export function PlatformService_GetInfrastructure(client: API.PlatformServiceClient, request: API.GetInfrastructureRequest, signal: AbortSignal) {
  return client.getInfrastructure(request, { signal });
}
export function RuleService_ListRules(client: API.RuleServiceClient, request: API.ListRulesRequest, signal: AbortSignal) {
  return client.listRules(request, { signal });
}
export function RuleService_GetRule(client: API.RuleServiceClient, request: API.GetRuleRequest, signal: AbortSignal) {
  return client.getRule(request, { signal });
}
export function RuleService_ListRuleVersions(client: API.RuleServiceClient, request: API.ListRuleVersionsRequest, signal: AbortSignal) {
  return client.listRuleVersions(request, { signal });
}
export function RuleService_ValidateRule(client: API.RuleServiceClient, request: API.ValidateRuleRequest, signal: AbortSignal) {
  return client.validateRule(request, { signal });
}
export function RuleService_SaveRule(client: API.RuleServiceClient, request: API.SaveRuleRequest, signal: AbortSignal) {
  return client.saveRule(request, { signal });
}
export function RuleService_RollbackRule(client: API.RuleServiceClient, request: API.RollbackRuleRequest, signal: AbortSignal) {
  return client.rollbackRule(request, { signal });
}
export function RuleService_DeleteRule(client: API.RuleServiceClient, request: API.DeleteRuleRequest, signal: AbortSignal) {
  return client.deleteRule(request, { signal });
}
export function RuleService_PauseRule(client: API.RuleServiceClient, request: API.PauseRuleRequest, signal: AbortSignal) {
  return client.pauseRule(request, { signal });
}
export function RuleService_ResumeRule(client: API.RuleServiceClient, request: API.ResumeRuleRequest, signal: AbortSignal) {
  return client.resumeRule(request, { signal });
}
export function RuleService_WatchRules(client: API.RuleServiceClient, request: API.WatchRulesRequest, signal: AbortSignal) {
  return client.watchRules(request, { signal });
}
export function RuleService_TestRule(client: API.RuleServiceClient, request: API.TestRuleRequest, signal: AbortSignal) {
  return client.testRule(request, { signal });
}
export function RuleService_ListRuleRuns(client: API.RuleServiceClient, request: API.ListRuleRunsRequest, signal: AbortSignal) {
  return client.listRuleRuns(request, { signal });
}
export function RuleService_GetRuleRun(client: API.RuleServiceClient, request: API.GetRuleRunRequest, signal: AbortSignal) {
  return client.getRuleRun(request, { signal });
}
export function RuleService_CancelRuleRun(client: API.RuleServiceClient, request: API.CancelRuleRunRequest, signal: AbortSignal) {
  return client.cancelRuleRun(request, { signal });
}
export function SessionService_ListSessions(client: API.SessionServiceClient, request: API.ListSessionsRequest, signal: AbortSignal) {
  return client.listSessions(request, { signal });
}
export function SessionService_RevokeSession(client: API.SessionServiceClient, request: API.RevokeSessionRequest, signal: AbortSignal) {
  return client.revokeSession(request, { signal });
}
export function SessionService_RevokeOtherSessions(client: API.SessionServiceClient, request: API.RevokeOtherSessionsRequest, signal: AbortSignal) {
  return client.revokeOtherSessions(request, { signal });
}
export function WiringService_GetWiringCatalog(client: API.WiringServiceClient, request: API.GetWiringCatalogRequest, signal: AbortSignal) {
  return client.getWiringCatalog(request, { signal });
}
export function WiringService_AnalyzeBinding(client: API.WiringServiceClient, request: API.AnalyzeBindingRequest, signal: AbortSignal) {
  return client.analyzeBinding(request, { signal });
}
export function WiringService_AnalyzeRule(client: API.WiringServiceClient, request: API.AnalyzeRuleRequest, signal: AbortSignal) {
  return client.analyzeRule(request, { signal });
}
export function WiringService_RenameStep(client: API.WiringServiceClient, request: API.RenameStepRequest, signal: AbortSignal) {
  return client.renameStep(request, { signal });
}
export function WorkflowService_ListWorkflows(client: API.WorkflowServiceClient, request: API.ListWorkflowsRequest, signal: AbortSignal) {
  return client.listWorkflows(request, { signal });
}
export function WorkflowService_StartWorkflow(client: API.WorkflowServiceClient, request: API.StartWorkflowRequest, signal: AbortSignal) {
  return client.startWorkflow(request, { signal });
}
export function WorkflowService_ListRuns(client: API.WorkflowServiceClient, request: API.ListRunsRequest, signal: AbortSignal) {
  return client.listRuns(request, { signal });
}
export function WorkflowService_GetRun(client: API.WorkflowServiceClient, request: API.GetRunRequest, signal: AbortSignal) {
  return client.getRun(request, { signal });
}
export function WorkflowService_CancelRun(client: API.WorkflowServiceClient, request: API.CancelRunRequest, signal: AbortSignal) {
  return client.cancelRun(request, { signal });
}
export function WorkflowService_TerminateRun(client: API.WorkflowServiceClient, request: API.TerminateRunRequest, signal: AbortSignal) {
  return client.terminateRun(request, { signal });
}
export function WorkflowService_SignalRun(client: API.WorkflowServiceClient, request: API.SignalRunRequest, signal: AbortSignal) {
  return client.signalRun(request, { signal });
}
export function ScheduleService_GetSchedule(client: API.ScheduleServiceClient, request: API.GetScheduleRequest, signal: AbortSignal) {
  return client.getSchedule(request, { signal });
}
export function ScheduleService_CreateSchedule(client: API.ScheduleServiceClient, request: API.CreateScheduleRequest, signal: AbortSignal) {
  return client.createSchedule(request, { signal });
}
export function ScheduleService_UpdateSchedule(client: API.ScheduleServiceClient, request: API.UpdateScheduleRequest, signal: AbortSignal) {
  return client.updateSchedule(request, { signal });
}
export function ScheduleService_DeleteSchedule(client: API.ScheduleServiceClient, request: API.DeleteScheduleRequest, signal: AbortSignal) {
  return client.deleteSchedule(request, { signal });
}
export function ScheduleService_ListSchedules(client: API.ScheduleServiceClient, request: API.ListSchedulesRequest, signal: AbortSignal) {
  return client.listSchedules(request, { signal });
}
export function ScheduleService_PauseSchedule(client: API.ScheduleServiceClient, request: API.PauseScheduleRequest, signal: AbortSignal) {
  return client.pauseSchedule(request, { signal });
}
export function ScheduleService_UnpauseSchedule(client: API.ScheduleServiceClient, request: API.UnpauseScheduleRequest, signal: AbortSignal) {
  return client.unpauseSchedule(request, { signal });
}
export function ScheduleService_TriggerSchedule(client: API.ScheduleServiceClient, request: API.TriggerScheduleRequest, signal: AbortSignal) {
  return client.triggerSchedule(request, { signal });
}
