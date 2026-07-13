export type Route =
  | { name: "tasks" }
  | { name: "new" }
  | { name: "task"; id: string }
  | { name: "confirm"; id: string }
  | { name: "spec"; id: string }
  | { name: "templateFill"; id: string }
  | { name: "preview"; id: string };

export function parseRoute(): Route {
  const hash = window.location.hash.replace(/^#\/?/, "");
  const parts = hash.split("/").filter(Boolean);
  if (parts.length === 0 || parts[0] === "tasks" && parts.length === 1) {
    return { name: "tasks" };
  }
  if (parts[0] === "new") {
    return { name: "new" };
  }
  if (parts[0] === "tasks" && parts[1]) {
    if (parts[2] === "confirm") {
      return { name: "confirm", id: parts[1] };
    }
    if (parts[2] === "spec") {
      return { name: "spec", id: parts[1] };
    }
    if (parts[2] === "template-fill") {
      return { name: "templateFill", id: parts[1] };
    }
    if (parts[2] === "preview") {
      return { name: "preview", id: parts[1] };
    }
    return { name: "task", id: parts[1] };
  }
  return { name: "tasks" };
}

export function go(route: Route) {
  window.location.hash = routeToHash(route);
}

export function replaceRoute(route: Route) {
  window.history.replaceState(window.history.state, "", routeToHash(route));
  window.dispatchEvent(new Event("hashchange"));
}

export function routeToHash(route: Route) {
  switch (route.name) {
    case "tasks":
      return "#/tasks";
    case "new":
      return "#/new";
    case "task":
      return `#/tasks/${route.id}`;
    case "confirm":
      return `#/tasks/${route.id}/confirm`;
    case "spec":
      return `#/tasks/${route.id}/spec`;
    case "templateFill":
      return `#/tasks/${route.id}/template-fill`;
    case "preview":
      return `#/tasks/${route.id}/preview`;
  }
}
