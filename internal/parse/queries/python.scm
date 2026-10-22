; See go.scm for capture conventions.

(class_definition name: (identifier) @name body: (_) @body) @def.class
(function_definition name: (identifier) @name body: (_) @body) @def.function
(module (expression_statement (assignment left: (identifier) @name) @def.var))

(class_definition superclasses: (argument_list [(identifier) (attribute)] @extends))

(call function: (identifier) @call.name) @call
(call function: (attribute object: (_) @call.qualifier attribute: (identifier) @call.name)) @call

(import_statement) @import
(import_from_statement) @import

(module
  (expression_statement
    (assignment
      left: (identifier) @_all
      right: (list (string (string_content) @export.name))))
  (#eq? @_all "__all__"))
