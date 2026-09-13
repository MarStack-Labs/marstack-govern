import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { CatalogService } from "./gen/marstack/govern/v1/catalog_pb";
import { SessionService } from "./gen/marstack/govern/v1/identity_pb";
import { RequestService } from "./gen/marstack/govern/v1/requests_pb";
import { DecisionService } from "./gen/marstack/govern/v1/decisions_pb";
import { AuditService } from "./gen/marstack/govern/v1/audit_pb";
import { FinOpsService } from "./gen/marstack/govern/v1/finops_pb";
import { PolicyService } from "./gen/marstack/govern/v1/policy_pb";
import { TenancyService } from "./gen/marstack/govern/v1/tenancy_pb";
import { TopologyService } from "./gen/marstack/govern/v1/topology_pb";

const transport = createConnectTransport({
  baseUrl: window.location.origin,
});

export const catalog = createClient(CatalogService, transport);
export const tenancy = createClient(TenancyService, transport);
export const sessions = createClient(SessionService, transport);
export const requests = createClient(RequestService, transport);
export const decisions = createClient(DecisionService, transport);
export const finops = createClient(FinOpsService, transport);
export const auditTrail = createClient(AuditService, transport);
export const policies = createClient(PolicyService, transport);
export const topology = createClient(TopologyService, transport);
