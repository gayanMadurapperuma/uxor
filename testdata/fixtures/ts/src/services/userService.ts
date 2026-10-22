import { Repo, type User } from "../repo";
import * as log from "@app/logging";
import defaultConfig from "./config";
import { BaseService } from "@app/base";

export interface UserLookup {
  getUserById(id: number): Promise<User | undefined>;
}

export type UserId = number;

export class UserService extends BaseService implements UserLookup {
  private cache = new Map<number, User>();

  constructor(private readonly repo: Repo) {
    super();
  }

  async getUserById(id: number): Promise<User | undefined> {
    log.debug("lookup", id);
    this.log("lookup");
    return this.repo.findById(id);
  }

  private cacheKey(id: number): string {
    return `user:${id}`;
  }
}

export const formatUser = (u: User): string => {
  return u.name.trim();
};

function internalHelper(x: number) {
  return defaultConfig.load().scale * x;
}

export default UserService;

export function lookup(svc: UserLookup, id: number) {
  return svc.getUserById(id);
}
