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
export function ErrorService_SearchErrorGroups(client: API.ErrorServiceClient, request: API.SearchErrorGroupsRequest, signal: AbortSignal) {
  return client.searchErrorGroups(request, { signal });
}
export function ErrorService_GetErrorGroup(client: API.ErrorServiceClient, request: API.GetErrorGroupRequest, signal: AbortSignal) {
  return client.getErrorGroup(request, { signal });
}
export function ErrorService_ErrorInfo(client: API.ErrorServiceClient, request: API.ErrorInfoRequest, signal: AbortSignal) {
  return client.errorInfo(request, { signal });
}
export function ErrorService_SetErrorGroupState(client: API.ErrorServiceClient, request: API.SetErrorGroupStateRequest, signal: AbortSignal) {
  return client.setErrorGroupState(request, { signal });
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
export function SessionService_ListSessions(client: API.SessionServiceClient, request: API.ListSessionsRequest, signal: AbortSignal) {
  return client.listSessions(request, { signal });
}
export function SessionService_RevokeSession(client: API.SessionServiceClient, request: API.RevokeSessionRequest, signal: AbortSignal) {
  return client.revokeSession(request, { signal });
}
export function SessionService_RevokeOtherSessions(client: API.SessionServiceClient, request: API.RevokeOtherSessionsRequest, signal: AbortSignal) {
  return client.revokeOtherSessions(request, { signal });
}
