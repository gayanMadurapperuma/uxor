import { UserService } from "./userService";

describe("UserService", () => {
  it("returns a user", async () => {
    const svc = new UserService(fakeRepo());
    expect(await svc.getUserById(1)).toBeDefined();
  });
});
