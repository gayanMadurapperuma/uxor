export default class Config {
  scale = 2;

  static load(): Config {
    return new Config();
  }
}
