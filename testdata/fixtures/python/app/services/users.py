"""User service."""
import os
import os.path as osp
from typing import Optional
from ..models import User, Base as ModelBase
from . import helpers

__all__ = ["UserService", "load_users"]

DEFAULT_LIMIT = 10


class UserService(ModelBase):
    """Loads users."""

    def __init__(self, repo):
        self.repo = repo

    def get_user_by_id(self, user_id: int) -> Optional[User]:
        user = self.repo.find(user_id)
        return helpers.clean(user)

    def _cache_key(self, user_id):
        return f"user:{user_id}"


def load_users(path):
    return [line for line in open(osp.join(path, "users.txt"))]


def _private_helper():
    def inner():
        return os.getcwd()
    return inner()


def describe(user: User) -> str:
    return user.display_name()
