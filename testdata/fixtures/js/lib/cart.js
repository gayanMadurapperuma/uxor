const { sum } = require("./math");
const util = require("util");

class Cart {
  constructor() {
    this.items = [];
  }

  addItem(item) {
    this.items.push(item);
  }

  total() {
    return sum(this.items.map((i) => i.price));
  }
}

function createCart() {
  return new Cart();
}

const formatTotal = function (cart) {
  return util.format("%d", cart.total());
};

module.exports = { Cart, createCart, formatTotal };
