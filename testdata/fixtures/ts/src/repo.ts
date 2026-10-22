export interface User {
  id: number;
  name: string;
}

export class Repo {
  findById(id: number): Promise<User | undefined> {
    return Promise.resolve(undefined);
  }
}
