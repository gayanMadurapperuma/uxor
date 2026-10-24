import React from "react";
import { UserService, formatUser } from "./services/userService";

export function App(props: { name: string }) {
  return <div className="app">{props.name}</div>;
}

export function AuthProvider(props: { service: UserService }) {
  const handleSignIn = async (id: number) => {
    const user = await props.service.getUserById(id);
    return user ? formatUser(user) : "";
  };
  const handleReset = function () {
    return "";
  };
  const label = "Sign in";
  return <button onClick={() => handleSignIn(1)}>{label}</button>;
}

export class Banner extends React.Component {
  handleDismiss = () => {
    this.setState({ open: false });
  };
}
