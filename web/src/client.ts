import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { CatalogService } from "./gen/marstack/govern/v1/catalog_pb";

const transport = createConnectTransport({
  baseUrl: window.location.origin,
});

export const catalog = createClient(CatalogService, transport);
