; See go.scm for capture conventions. Used for both .ts and .tsx.

(function_declaration name: (identifier) @name body: (_) @body) @def.function
(generator_function_declaration name: (identifier) @name body: (_) @body) @def.function
(class_declaration name: (type_identifier) @name body: (_) @body) @def.class
(abstract_class_declaration name: (type_identifier) @name body: (_) @body) @def.class
(method_definition name: (_) @name body: (_) @body) @def.method
(abstract_method_signature name: (_) @name) @def.method
(interface_declaration name: (type_identifier) @name body: (_) @body) @def.interface
(interface_body (method_signature name: (_) @name) @def.method)
(type_alias_declaration name: (type_identifier) @name) @def.type
(enum_declaration name: (identifier) @name body: (_) @body) @def.type

(program
  (lexical_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)])) @def.function)
(program
  (export_statement
    (lexical_declaration
      (variable_declarator
        name: (identifier) @name
        value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)])) @def.function))
(program (lexical_declaration (variable_declarator name: (identifier) @name)) @def.var)
(program (export_statement (lexical_declaration (variable_declarator name: (identifier) @name)) @def.var))
(program (variable_declaration (variable_declarator name: (identifier) @name)) @def.var)

; Named function-valued declarations inside any block: helpers inside
; functions and React handlers inside components (D30). Plain values and
; anonymous callbacks stay out.
(statement_block
  (lexical_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)])) @def.function)
(statement_block
  (variable_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)])) @def.function)
; Class fields holding functions (handleClick = () => {…}) are methods.
(public_field_definition
  name: (property_identifier) @name
  value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)]) @def.method

(call_expression
  function: (identifier) @_fn
  arguments: (arguments . (string (string_fragment) @name))
  (#match? @_fn "^(describe|it|test)$")) @def.test

(class_heritage (extends_clause value: (_) @extends))
(class_heritage (implements_clause (_) @implements))
(extends_type_clause (_) @extends)

(call_expression function: (identifier) @call.name) @call
(call_expression
  function: (member_expression object: (_) @call.qualifier property: (property_identifier) @call.name)) @call
(new_expression constructor: (identifier) @call.name) @call

(import_statement) @import
(export_statement source: (_)) @import
(call_expression
  function: (identifier) @_req
  arguments: (arguments . (string))
  (#eq? @_req "require")) @import
(call_expression function: (import) arguments: (arguments . (string))) @import

(export_statement (export_clause (export_specifier name: (_) @export.name)) !source)
(export_statement value: (identifier) @export.name)
