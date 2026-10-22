import React from "react";
import { UserService } from "./services/userService";

export function App(props: { name: string }) {
  return <div className="app">{props.name}</div>;
}
