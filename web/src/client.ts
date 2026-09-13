import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { CatalogService } from "./gen/marstack/govern/v1/catalog_pb";
import { TenancyService } from "./gen/marstack/govern/v1/tenancy_pb";

const transport = createConnectTransport({
  baseUrl: window.location.origin,
});

export const catalog = createClient(CatalogService, transport);
export const tenancy = createClient(TenancyService, transport);
