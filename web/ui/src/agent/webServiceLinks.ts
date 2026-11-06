import { validWebServiceId } from "@/api/webServices";
export interface WebServiceLink { present: boolean; sessionId: string | null; serviceId: string | null }
export function webServiceLink(search: string): WebServiceLink {
  const p = new URLSearchParams(search);
  const serviceId = p.get("web_service"), sessionId = p.get("c");
  if (!p.has("web_service")) return { present: false, sessionId: null, serviceId: null };
  return { present: true, sessionId: p.getAll("c").length === 1 && validWebServiceId(sessionId) ? sessionId : null,
    serviceId: p.getAll("web_service").length === 1 && validWebServiceId(serviceId) ? serviceId : null };
}
