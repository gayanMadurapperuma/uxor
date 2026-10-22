def clean(user):
    if user is None:
        return None
    user.save()
    return user
